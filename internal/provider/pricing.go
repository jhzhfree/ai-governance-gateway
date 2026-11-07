package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
)

// CachePricing is an explicitly reviewed enterprise tariff, not a vendor invoice.
// Both zero prices are valid explicit free tiers; nil means uniform input pricing.
type CacheWriteTTL struct {
	FiveMinutePerMillion int64 `json:"five_minute_per_million"`
	OneHourPerMillion    int64 `json:"one_hour_per_million"`
}

func (p *CacheWriteTTL) UnmarshalJSON(raw []byte) error {
	var v struct {
		Five *int64 `json:"five_minute_per_million"`
		Hour *int64 `json:"one_hour_per_million"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(&struct{}{}) != io.EOF || v.Five == nil || v.Hour == nil {
		return errors.New("both cache TTL rates must be explicitly configured")
	}
	p.FiveMinutePerMillion, p.OneHourPerMillion = *v.Five, *v.Hour
	if !p.valid() {
		return errors.New("invalid cache TTL tariff")
	}
	return nil
}
func (p *CacheWriteTTL) valid() bool {
	return p != nil && p.FiveMinutePerMillion >= 0 && p.FiveMinutePerMillion <= MaxAmount && p.OneHourPerMillion >= 0 && p.OneHourPerMillion <= MaxAmount
}

// CacheWriteUsage contains explicit original counts, including proven zeros.
type CacheWriteUsage struct {
	FiveMinuteTokens *int64 `json:"ephemeral_5m_input_tokens"`
	OneHourTokens    *int64 `json:"ephemeral_1h_input_tokens"`
}

func (u *CacheWriteUsage) valid(write int64) bool {
	return u != nil && u.FiveMinuteTokens != nil && u.OneHourTokens != nil && *u.FiveMinuteTokens >= 0 && *u.FiveMinuteTokens <= write && *u.OneHourTokens >= 0 && *u.OneHourTokens == write-*u.FiveMinuteTokens
}

type CachePricing struct {
	WriteTTL        *CacheWriteTTL `json:"write_ttl,omitempty"`
	ReadPerMillion  int64          `json:"read_per_million"`
	WritePerMillion int64          `json:"write_per_million"`
}

// JSON publication requires both rates explicitly; omission is not a free tier.
func (p *CachePricing) UnmarshalJSON(raw []byte) error {
	var value struct {
		TTL   *CacheWriteTTL `json:"write_ttl"`
		Read  *int64         `json:"read_per_million"`
		Write *int64         `json:"write_per_million"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Read == nil || value.Write == nil {
		return errors.New("both cache rates must be explicitly configured")
	}
	p.WriteTTL = value.TTL
	p.ReadPerMillion = *value.Read
	p.WritePerMillion = *value.Write
	if !p.valid() {
		return errors.New("invalid cache tariff")
	}
	return nil
}

func (p *CachePricing) valid() bool {
	return p != nil && (p.WriteTTL == nil || p.WriteTTL.valid()) && p.ReadPerMillion >= 0 && p.ReadPerMillion <= MaxAmount && p.WritePerMillion >= 0 && p.WritePerMillion <= MaxAmount
}
func (p *CachePricing) clone() *CachePricing {
	if p == nil {
		return nil
	}
	copy := *p
	if p.WriteTTL != nil {
		ttl := *p.WriteTTL
		copy.WriteTTL = &ttl
	}
	return &copy
}

// TokenCost applies one final upward micro-currency rounding to disjoint usage.
// The same arithmetic is used during live settlement and crash recovery.
func TokenCost(input, output, read, write, inPrice, outPrice int64, cache *CachePricing) (int64, error) {
	return TokenCostDetailed(input, output, read, write, inPrice, outPrice, cache, nil)
}

// TokenCostDetailed prices cache durations only when every count is evidenced.
func TokenCostDetailed(input, output, read, write, inPrice, outPrice int64, cache *CachePricing, ttl *CacheWriteUsage) (int64, error) {
	const maximum = int64(1<<31 - 1)
	if input < 0 || output < 0 || read < 0 || write < 0 || input > maximum || output > maximum || read > input || write > input-read || inPrice < 0 || inPrice > MaxAmount || outPrice < 0 || outPrice > MaxAmount || (cache != nil && !cache.valid()) {
		return 0, errors.New("invalid tariff or token quantities")
	}
	if (ttl != nil && !ttl.valid(write)) || (cache != nil && cache.WriteTTL != nil && !ttl.valid(write)) {
		return 0, errors.New("missing or invalid cache TTL quantities")
	}
	amount := new(big.Int)
	if cache == nil {
		amount.Mul(big.NewInt(input), big.NewInt(inPrice))
	} else {
		amount.Mul(big.NewInt(input-read-write), big.NewInt(inPrice))
		amount.Add(amount, new(big.Int).Mul(big.NewInt(read), big.NewInt(cache.ReadPerMillion)))
		if cache.WriteTTL == nil {
			amount.Add(amount, new(big.Int).Mul(big.NewInt(write), big.NewInt(cache.WritePerMillion)))
		} else {
			amount.Add(amount, new(big.Int).Mul(big.NewInt(*ttl.FiveMinuteTokens), big.NewInt(cache.WriteTTL.FiveMinutePerMillion)))
			amount.Add(amount, new(big.Int).Mul(big.NewInt(*ttl.OneHourTokens), big.NewInt(cache.WriteTTL.OneHourPerMillion)))
		}
	}
	amount.Add(amount, new(big.Int).Mul(big.NewInt(output), big.NewInt(outPrice)))
	amount.Add(amount, big.NewInt(999999))
	amount.Quo(amount, big.NewInt(1000000))
	if !amount.IsInt64() || amount.Int64() > MaxAmount {
		return 0, errors.New("configured price exceeds safe accounting range")
	}
	return amount.Int64(), nil
}
