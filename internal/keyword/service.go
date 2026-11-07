// Package keyword implements bounded rule matching without storing matched text.
package keyword

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrBlocked = errors.New("keyword policy blocked content")
var ErrStreamPolicy = errors.New("stream requires literal alert-only output rules")
var ErrInvalid = errors.New("invalid keyword policy")

type Rule struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Pattern   string `json:"pattern"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
}
type Policy struct {
	Revision int64  `json:"revision"`
	Rules    []Rule `json:"rules"`
}
type compiled struct {
	Rule
	re *regexp.Regexp
}

var ruleID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func Compile(rules []Rule) ([]compiled, error) {
	if len(rules) > 32 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	out := []compiled{}
	for _, r := range rules {
		if !ruleID.MatchString(r.ID) || seen[r.ID] || len(r.Pattern) < 1 || len(r.Pattern) > 256 || (r.Direction != "input" && r.Direction != "output" && r.Direction != "both") || (r.Action != "alert" && r.Action != "block" && r.Action != "redact") || (r.Action == "redact" && r.Direction != "output") {
			return nil, ErrInvalid
		}
		seen[r.ID] = true
		c := compiled{Rule: r}
		switch r.Kind {
		case "literal":
			c.re = regexp.MustCompile(regexp.QuoteMeta(r.Pattern))
		case "regex":
			var err error
			c.re, err = regexp.Compile(r.Pattern)
			if err != nil {
				return nil, ErrInvalid
			}
			tree, e := syntax.Parse(r.Pattern, syntax.Perl)
			if e != nil || minimumWidth(tree) == 0 {
				return nil, ErrInvalid
			}
		default:
			return nil, ErrInvalid
		}
		out = append(out, c)
	}
	return out, nil
}

type query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Read(ctx context.Context, q query, org, project string) (Policy, error) {
	p := Policy{Rules: []Rule{}}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT revision,rules FROM keyword_policies WHERE organization_id=$1 AND project_id=$2`, org, project).Scan(&p.Revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if json.Unmarshal(raw, &p.Rules) != nil {
		return p, ErrInvalid
	}
	_, err = Compile(p.Rules)
	return p, err
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool} }

type Session struct {
	service               *Service
	org, project, request string
	revision              int64
	rules                 []compiled
	seen                  map[string]bool
	carry                 map[string]string
}

func (s *Service) Begin(ctx context.Context, org, project, request string, stream bool) (*Session, error) {
	p, err := Read(ctx, s.pool, org, project)
	if err != nil {
		return nil, err
	}
	rules, err := Compile(p.Rules)
	if err != nil {
		return nil, err
	}
	if stream {
		for _, r := range rules {
			if r.Direction != "input" && (r.Action != "alert" || r.Kind != "literal") {
				return nil, ErrStreamPolicy
			}
		}
	}
	return &Session{service: s, org: org, project: project, request: request, revision: p.Revision, rules: rules, seen: map[string]bool{}, carry: map[string]string{}}, nil
}
func (s *Session) Process(ctx context.Context, direction, text string) (string, error) {
	if s == nil {
		return text, nil
	}
	matches := []compiled{}
	blocked := false
	// Match the original text; earlier redactions cannot hide later rule matches.
	for _, rule := range s.rules {
		if rule.Direction != direction && rule.Direction != "both" {
			continue
		}
		if rule.re.MatchString(text) {
			matches = append(matches, rule)
			blocked = blocked || rule.Action == "block"
		}
	}
	if len(matches) == 0 {
		return text, nil
	}
	newMatch := false
	for _, r := range matches {
		if !s.seen[direction+"/"+r.ID] {
			newMatch = true
		}
	}
	var err error
	if newMatch {
		err = pgx.BeginFunc(ctx, s.service.pool, func(tx pgx.Tx) error {
			for _, r := range matches {
				key := direction + "/" + r.ID
				if s.seen[key] {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO keyword_matches(organization_id,project_id,request_id,policy_revision,rule_id,direction,action) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(request_id,rule_id,direction) DO NOTHING`, s.org, s.project, s.request, s.revision, r.ID, direction, r.Action); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		return "", err
	}
	for _, r := range matches {
		s.seen[direction+"/"+r.ID] = true
	}
	if blocked {
		return "", ErrBlocked
	}
	mask := make([]bool, len(text))
	redacted := false
	for _, r := range matches {
		if r.Action == "redact" {
			ranges := r.re.FindAllStringIndex(text, 4097)
			if len(ranges) > 4096 {
				return "", ErrInvalid
			}
			for _, span := range ranges {
				for i := span[0]; i < span[1]; i++ {
					mask[i] = true
					redacted = true
				}
			}
		}
	}
	if !redacted {
		return text, nil
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		if mask[i] {
			out.WriteString("[REDACTED]")
			for i < len(text) && mask[i] {
				i++
			}
		} else {
			out.WriteByte(text[i])
			i++
		}
	}
	return out.String(), nil
}

// Stream detects literal matches across delta boundaries with at most 255 bytes
// of carry per output channel. It never attempts to retract emitted content.
func (s *Session) Stream(ctx context.Context, channel, text string) error {
	if s == nil || text == "" {
		return nil
	}
	window := s.carry[channel] + text
	if _, err := s.Process(ctx, "output", window); err != nil {
		return err
	}
	max := 0
	for _, r := range s.rules {
		if r.Direction != "input" && len(r.Pattern) > max {
			max = len(r.Pattern)
		}
	}
	if max > 1 && len(window) >= max {
		s.carry[channel] = window[len(window)-max+1:]
	} else if max > 1 {
		s.carry[channel] = window
	}
	return nil
}
func IsBlocked(err error) bool { return errors.Is(err, ErrBlocked) }
func minimumWidth(r *syntax.Regexp) int {
	switch r.Op {
	case syntax.OpLiteral:
		return len(r.Rune)
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 1
	case syntax.OpCapture, syntax.OpPlus:
		return minimumWidth(r.Sub[0])
	case syntax.OpRepeat:
		return r.Min * minimumWidth(r.Sub[0])
	case syntax.OpConcat:
		n := 0
		for _, s := range r.Sub {
			n += minimumWidth(s)
		}
		return n
	case syntax.OpAlternate:
		n := int(^uint(0) >> 1)
		for _, s := range r.Sub {
			if w := minimumWidth(s); w < n {
				n = w
			}
		}
		return n
	}
	return 0
}

// AllowsOpaqueOutput fails closed when a configured text-output policy cannot
// inspect a vector or other non-text response.
func (s *Session) AllowsOpaqueOutput() bool {
	if s == nil {
		return true
	}
	for _, rule := range s.rules {
		if rule.Direction != "input" {
			return false
		}
	}
	return true
}
