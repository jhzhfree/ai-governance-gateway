package provider

import "errors"

// ValidateEmbeddedEmbedding narrows the common text schema to reviewed fixed
// supplier wire contracts. Model/account limits still require deployment review.
func ValidateEmbeddedEmbedding(supplier string, req Request) error {
	invalid := errors.New("unsupported embedded embedding request")
	if !validEmbeddingRequest(req) {
		return invalid
	}
	switch supplier {
	case "aliyun":
		if len(req.Input) > 10 {
			return invalid
		}
		if req.Dimensions != nil {
			switch *req.Dimensions {
			case 64, 128, 256, 512, 768, 1024, 1536, 2048:
			default:
				return invalid
			}
		}
	case "zhipu":
		if req.Dimensions != nil && (*req.Dimensions < 256 || *req.Dimensions > 2048) {
			return invalid
		}
	case "volcengine":
		if req.Dimensions != nil {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
