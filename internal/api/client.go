package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/pathbird/pbauthor/internal/config"
	log "github.com/sirupsen/logrus"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"

	"github.com/pkg/errors"
)

type Client struct {
	authToken  string
	host       string
	httpClient *http.Client
}

func New(authToken string) *Client {
	log.Debugf("creating API client using host: %s", config.PathbirdApiHost)
	return &Client{
		authToken:  authToken,
		host:       fmt.Sprintf("%s/api", config.PathbirdApiHost),
		httpClient: http.DefaultClient,
	}
}

type request struct {
	route string
	body  interface{}
}

type response struct {
	route        string
	httpResponse *http.Response
}

func (r *response) ReadBytes() ([]byte, error) {
	respData, err := ioutil.ReadAll(r.httpResponse.Body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read api response")
	}
	return respData, nil
}

type StatusError struct {
	res   *response
	error *ErrorResponse
}

func (s StatusError) Error() string {
	return fmt.Sprintf(
		"api endpoint (%s) returned error status: %s (%s): %s",
		s.res.route, s.res.httpResponse.Status, s.error.Error,
		s.Description(),
	)
}

func (s StatusError) Description() string {
	d := s.error.Message
	if d == "" {
		return "<unknown>"
	}
	return d
}

var _ error = (*StatusError)(nil)

// Return an error if the HTTP status code indicates an error (i.e., 4xx or 5xx code).
func (r *response) StatusError() (*StatusError, error) {
	if r.httpResponse.StatusCode >= 400 {
		errorResponse, err := r.unmarshalErrorBody()
		if err != nil {
			return nil, err
		}
		log.Debugf("error (%d): %s", r.httpResponse.StatusCode, errorResponse.Verbose())
		return &StatusError{
			res:   r,
			error: errorResponse,
		}, nil
	}
	return nil, nil
}

// A generic error returned by the API
// Consumers can inspect the value of `Error` and unmarshall `Details` as appropriate.
type ErrorResponse struct {
	Error   string          `json:"error"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details"`
}

func (r *ErrorResponse) String() string {
	return fmt.Sprintf("error (%s): %s", r.Error, r.Message)
}

func (r *ErrorResponse) Verbose() string {
	msg := fmt.Sprintf("error (%s): %s", r.Error, r.Message)
	if string(r.Details) != "" {
		msg += fmt.Sprintf(" (details: %s)", r.Details)
	}
	return msg
}

func (r *response) unmarshalErrorBody() (*ErrorResponse, error) {
	contentType := r.httpResponse.Header.Get("Content-Type")
	if !isJsonContentType(contentType) {
		switch r.httpResponse.StatusCode {
		case 413:
			return &ErrorResponse{
				Error:   "PayloadTooLarge",
				Message: "the request payload was too large",
				Details: nil,
			}, nil
		}
		return nil, errors.Errorf("unknown api error response (status: %s, content-type: %s)", r.httpResponse.Status, contentType)
	}

	var errorResponse ErrorResponse
	if err := r.UnmarshalJson(&errorResponse); err != nil {
		return nil, err
	}
	if errorResponse.Error == "" {
		return nil, errors.New("api error response did not include \"error\" key")
	}
	return &errorResponse, nil
}

var _ io.Closer = (*response)(nil)

func (r *response) Close() error {
	return r.httpResponse.Body.Close()
}

func (r *response) UnmarshalJson(target interface{}) error {
	data, err := r.ReadBytes()
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, target)
	if err != nil {
		return errors.Wrap(err, "unable to unmarshal json response")
	}
	return nil
}

const userAgent = `pbauthor`

func (c *Client) postJson(r *request) (*response, error) {
	reqBody, err := json.Marshal(r.body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal request")
	}

	endpoint := fmt.Sprintf("%s/%s", c.host, r.route)
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	httpResponse, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "request to api failed")
	}

	return &response{
		route:        r.route,
		httpResponse: httpResponse,
	}, nil
}

type multipartRequest struct {
	route  string
	fields map[string]string
	files  []FileRef
}

func (c *Client) postMultipart(r *multipartRequest) (*response, error) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for fieldname, value := range r.fields {
		log.Debugf("setting field: %s", fieldname)
		err := w.WriteField(fieldname, value)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create api request body")
		}
	}
	for _, file := range r.files {
		// We just always add files underneath the "files" key since that's what every
		// Pathbird API endpoint expects
		log.Debugf("adding file: %s", file.Name)
		err := file.addToWriter("files", w)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create api request body")
		}
	}
	err := w.Close()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create api request body")
	}

	endpoint := fmt.Sprintf("%s/%s", c.host, r.route)
	req, err := http.NewRequest("POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	httpResponse, err := c.do(req)
	if err != nil {
		return nil, err
	}

	return &response{
		route:        r.route,
		httpResponse: httpResponse,
	}, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	if c.authToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}
	log.Debugf("sending api request (%s)", req.URL.Path)
	httpResponse, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "request to api failed")
	}
	log.Debugf("got api response (%s): %s", req.URL.Path, httpResponse.Status)
	return httpResponse, err
}
