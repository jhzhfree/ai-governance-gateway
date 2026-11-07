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
type CachePricing struct {
	ReadPerMillion  int64 `json:"read_per_million"`
	WritePerMillion int64 `json:"write_per_million"`
}

// JSON publication requires both rates explicitly; omission is not a free tier.
func (p *CachePricing) UnmarshalJSON(raw []byte) error {
	var value struct {
		Read  *int64 `json:"read_per_million"`
		Write *int64 `json:"write_per_million"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || value.Read == nil || value.Write == nil {
		return errors.New("both cache rates must be explicitly configured")
	}
	p.ReadPerMillion = *value.Read
	p.WritePerMillion = *value.Write
	if !p.valid() {
		return errors.New("invalid cache tariff")
	}
	return nil
}

func (p *CachePricing) valid() bool {
	return p != nil && p.ReadPerMillion >= 0 && p.ReadPerMillion <= MaxAmount && p.WritePerMillion >= 0 && p.WritePerMillion <= MaxAmount
}
func (p *CachePricing) clone() *CachePricing {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// TokenCost applies one final upward micro-currency rounding to disjoint usage.
// The same arithmetic is used during live settlement and crash recovery.
func TokenCost(input, output, read, write, inPrice, outPrice int64, cache *CachePricing) (int64, error) {
	const maximum = int64(1<<31 - 1)
	if input < 0 || output < 0 || read < 0 || write < 0 || input > maximum || output > maximum || read > input || write > input-read || inPrice < 0 || inPrice > MaxAmount || outPrice < 0 || outPrice > MaxAmount || (cache != nil && !cache.valid()) {
		return 0, errors.New("invalid tariff or token quantities")
	}
	amount := new(big.Int)
	if cache == nil {
		amount.Mul(big.NewInt(input), big.NewInt(inPrice))
	} else {
		amount.Mul(big.NewInt(input-read-write), big.NewInt(inPrice))
		amount.Add(amount, new(big.Int).Mul(big.NewInt(read), big.NewInt(cache.ReadPerMillion)))
		amount.Add(amount, new(big.Int).Mul(big.NewInt(write), big.NewInt(cache.WritePerMillion)))
	}
	amount.Add(amount, new(big.Int).Mul(big.NewInt(output), big.NewInt(outPrice)))
	amount.Add(amount, big.NewInt(999999))
	amount.Quo(amount, big.NewInt(1000000))
	if !amount.IsInt64() || amount.Int64() > MaxAmount {
		return 0, errors.New("configured price exceeds safe accounting range")
	}
	return amount.Int64(), nil
}
