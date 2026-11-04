// Package billing parses the operator-normalized statement format, not vendor exports.
package billing

import (
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const MaxCSV = 24576
const MaxRows = 100

var ErrInvalid = errors.New("invalid normalized statement")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
var period = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
var decimal = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

type Line struct {
	RecordID     string
	RequestID    string
	Period       string
	Currency     string
	Amount       int64
	InputTokens  int64
	OutputTokens int64
}

func Identifier(v string) bool { return identifier.MatchString(v) }
func Parse(text string) ([]Line, error) {
	if len(text) == 0 || len(text) > MaxCSV {
		return nil, ErrInvalid
	}
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = 7
	header, err := r.Read()
	if err != nil || strings.Join(header, ",") != "record_id,request_id,period,currency,amount,input_tokens,output_tokens" {
		return nil, ErrInvalid
	}
	lines := []Line{}
	seen := map[string]bool{}
	number := func(s string) (int64, error) {
		if !decimal.MatchString(s) {
			return 0, ErrInvalid
		}
		v, e := strconv.ParseInt(s, 10, 64)
		if e != nil || v > 9007199254740991 {
			return 0, ErrInvalid
		}
		return v, nil
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(lines) >= MaxRows {
			return nil, ErrInvalid
		}
		if !Identifier(row[0]) || seen[row[0]] || (row[1] != "" && !Identifier(row[1])) || !period.MatchString(row[2]) || row[3] != "CNY_MICRO" {
			return nil, ErrInvalid
		}
		amount, e := number(row[4])
		if e != nil {
			return nil, e
		}
		in, e := number(row[5])
		if e != nil || in > 2000000 {
			return nil, ErrInvalid
		}
		out, e := number(row[6])
		if e != nil || out > 1000000 {
			return nil, ErrInvalid
		}
		seen[row[0]] = true
		lines = append(lines, Line{row[0], row[1], row[2], row[3], amount, in, out})
	}
	if len(lines) == 0 {
		return nil, ErrInvalid
	}
	return lines, nil
}
