package onramp

import "errors"

// APIError 只携带可以公开给调用方的信息，不透传供应商响应与凭据。
type APIError struct {
	Code, Message string
	HTTP          int
	Retriable     bool
}

func (e *APIError) Error() string { return e.Code }
func validation(message string) error {
	return &APIError{Code: "VALIDATION", Message: message, HTTP: 400}
}
func providerError(err error) error {
	if err == nil || errors.Is(err, ErrPending) {
		return err
	}
	var safe *APIError
	if errors.As(err, &safe) {
		return err
	}
	return &APIError{Code: "PROVIDER_UNAVAILABLE", Message: "暂时无法核对供应商结果，请保留原订单，稍后重新查询", HTTP: 503, Retriable: true}
}
