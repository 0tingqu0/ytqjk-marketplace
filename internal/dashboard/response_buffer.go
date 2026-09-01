package dashboard

import (
	"bytes"
	"net/http"
)

type deferredHTTPResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newDeferredHTTPResponse() *deferredHTTPResponse {
	return &deferredHTTPResponse{header: make(http.Header)}
}

func (response *deferredHTTPResponse) Header() http.Header {
	return response.header
}

func (response *deferredHTTPResponse) WriteHeader(status int) {
	if response.status == 0 {
		response.status = status
	}
}

func (response *deferredHTTPResponse) Write(data []byte) (int, error) {
	if response.status == 0 {
		response.status = http.StatusOK
	}
	return response.body.Write(data)
}

func (response *deferredHTTPResponse) flush(writer http.ResponseWriter) error {
	for key, values := range response.header {
		writer.Header()[key] = append([]string(nil), values...)
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	writer.WriteHeader(status)
	if response.body.Len() == 0 {
		return nil
	}
	_, err := writer.Write(response.body.Bytes())
	return err
}

var _ http.ResponseWriter = (*deferredHTTPResponse)(nil)
