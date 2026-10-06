// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	httpMethodGet    = http.MethodGet
	maxQueryBytes    = 4 << 10
	maxPathParamSize = 256
	maxRouteSize     = 512
	maxProblemText   = 512
)

var (
	routeParamPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	routeSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.~-]+$`)
	credentialPattern   = regexp.MustCompile(`(?i)\b(authorization|client[_-]?secret|password|passwd|token|secret|api[_-]?key)\b(\s*[:=]\s*|\s+)(bearer\s+)?("[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerPattern       = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	userInfoPattern     = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)
	querySecretPattern  = regexp.MustCompile(`(?i)([?&](?:access_token|api[_-]?key|client[_-]?secret|password|passwd|secret|token)=)[^&#\s]*`)
)

type httpGateway struct {
	client           *resty.Client
	baseURL          string
	timeout          time.Duration
	maxResponseBytes int
	readRetries      int
	propagator       propagation.TextMapPropagator
	tracer           trace.Tracer
	requests         metric.Int64Counter
	duration         metric.Float64Histogram
}

type gatewayResponse struct {
	result      Result
	body        []byte
	isAccepted  bool
	isHTTPError bool
	method      string
	route       string
}

// APIError is an Ara problem response with bounded, credential-scrubbed text.
type APIError struct {
	Status int
	Type   string
	Title  string
	Detail string
}

func (e *APIError) Error() string {
	message := fmt.Sprintf("ara returned http status %d", e.Status)
	if e.Title != "" {
		message += ": " + e.Title
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

// RequestError reports a bounded HTTP failure class and preserves errors.Is/As.
type RequestError struct {
	Class   string
	Outcome Outcome
	cause   error
}

func (e *RequestError) Error() string { return "ara request " + e.Class }

func (e *RequestError) Unwrap() error { return e.cause }

func newHTTPGateway(config Config) (*httpGateway, error) {
	baseURL, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	meter := config.Meter
	if meter == nil {
		meter = metricnoop.NewMeterProvider().Meter("github.com/cavenine/ara-mcp/internal/ara/http")
	}
	tracer := config.Tracer
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer("github.com/cavenine/ara-mcp/internal/ara/http")
	}
	propagator := config.Propagator
	if propagator == nil {
		propagator = propagation.TraceContext{}
	}
	requests, err := meter.Int64Counter("ara.http.requests", metric.WithUnit("{request}"), metric.WithDescription("Ara HTTP request attempts by bounded outcome"))
	if err != nil {
		return nil, fmt.Errorf("ara http gateway: create request counter: %w", err)
	}
	duration, err := meter.Float64Histogram("ara.http.request.duration", metric.WithUnit("s"), metric.WithDescription("Ara HTTP request attempt duration"))
	if err != nil {
		return nil, fmt.Errorf("ara http gateway: create request duration histogram: %w", err)
	}
	client := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(config.Timeout).
		SetRetryCount(0).
		SetRedirectPolicy(resty.NoRedirectPolicy())
	return &httpGateway{
		client:           client,
		baseURL:          baseURL,
		timeout:          config.Timeout,
		maxResponseBytes: int(config.MaxResponseBytes),
		readRetries:      config.ReadRetries,
		propagator:       propagator,
		tracer:           tracer,
		requests:         requests,
		duration:         duration,
	}, nil
}

func (g *httpGateway) do(ctx context.Context, input request, target any) (gatewayResponse, error) {
	method, err := g.validate(input)
	response := gatewayResponse{method: g.methodLabel(input.Method), route: g.routeLabel(input.Route)}
	if err != nil {
		response.result.Outcome = OutcomeFailed
		return response, &RequestError{Class: "invalid_request", Outcome: OutcomeFailed, cause: err}
	}
	response.method, response.route = method, input.Route
	if err := ctx.Err(); err != nil {
		response.result.Outcome = g.responseFailureOutcome(method, 0)
		return response, &RequestError{Class: g.errorClass(err), Outcome: response.result.Outcome, cause: err}
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	attempts := 1
	if method == http.MethodGet {
		attempts += g.readRetries
	}
	for attempt := 0; attempt < attempts; attempt++ {
		response, err = g.doAttempt(ctx, input, method, target)
		if ctx.Err() != nil || !g.shouldRetry(input, response, err) || attempt+1 == attempts {
			if err == nil && response.isHTTPError {
				return response, parseAPIError(response.result.Status, response.body)
			}
			return response, err
		}
		timer := time.NewTimer(g.retryDelay(attempt))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			cause := ctx.Err()
			response.result = Result{Outcome: OutcomeFailed}
			return response, &RequestError{Class: g.errorClass(cause), Outcome: OutcomeFailed, cause: cause}
		case <-timer.C:
		}
	}
	response.result = Result{Outcome: OutcomeFailed}
	return response, errors.New("ara http gateway: retry attempts exhausted")
}

func (g *httpGateway) doAttempt(ctx context.Context, input request, method string, target any) (response gatewayResponse, returnedErr error) {
	started := time.Now()
	response.method, response.route = method, input.Route
	response.result.Outcome = OutcomeFailed
	statusClass, errorClass := "none", "none"
	ctx, span := g.tracer.Start(ctx, "ara.http.request", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", input.Route),
	))
	defer func() {
		if returnedErr != nil {
			var requestError *RequestError
			if errors.As(returnedErr, &requestError) {
				errorClass = requestError.Class
			} else {
				errorClass = "upstream"
			}
			span.RecordError(errors.New(returnedErr.Error()))
			span.SetStatus(codes.Error, returnedErr.Error())
		} else if response.isHTTPError {
			errorClass = "upstream"
			message := fmt.Sprintf("ara returned http status %d", response.result.Status)
			span.RecordError(errors.New(message))
			span.SetStatus(codes.Error, message)
		}
		span.SetAttributes(
			attribute.Int("http.response.status_code", response.result.Status),
			attribute.String("ara.outcome", string(response.result.Outcome)),
			attribute.String("http.response.status_class", statusClass),
		)
		span.End()
		attrs := metric.WithAttributes(
			attribute.String("http.request.method", method),
			attribute.String("http.route", input.Route),
			attribute.String("http.response.status_class", statusClass),
			attribute.String("ara.outcome", string(response.result.Outcome)),
			attribute.String("error.type", errorClass),
		)
		g.requests.Add(ctx, 1, attrs)
		g.duration.Record(ctx, time.Since(started).Seconds(), attrs)
	}()

	headers := make(http.Header)
	if input.RequestID != "" {
		headers.Set("X-Request-ID", input.RequestID)
	}
	if input.IdempotencyKey != "" {
		headers.Set("Idempotency-Key", input.IdempotencyKey)
	}
	g.propagator.Inject(ctx, propagation.HeaderCarrier(headers))
	responseLimit := g.maxResponseBytes
	if input.MaxResponseBytes > 0 && input.MaxResponseBytes < responseLimit {
		responseLimit = input.MaxResponseBytes
	}
	request := g.client.R().
		SetContext(ctx).
		SetPathParams(input.PathParams).
		SetResponseBodyLimit(responseLimit)
	for key, values := range headers {
		request.SetHeader(key, strings.Join(values, ", "))
	}
	if len(input.Body) > 0 {
		request.SetHeader("Content-Type", "application/json").SetBody(bytes.Clone(input.Body))
	}
	if query := queryValues(input.Query).Encode(); query != "" {
		request.SetQueryString(query)
	}

	resp, err := request.Execute(method, input.Route)
	if resp != nil {
		response.result.Status = resp.StatusCode()
		response.result.RequestID = safeRequestID(resp.Header().Get("X-Request-ID"))
		response.body = resp.Body()
		response.result.Outcome = g.httpOutcome(method, response.result.Status)
		response.isAccepted = response.result.Status == http.StatusAccepted
		response.isHTTPError = response.result.Status < http.StatusOK || response.result.Status >= http.StatusMultipleChoices
		response.result.RetrySafe = g.canRetryMutation(input, response.result.Outcome)
		statusClass = g.statusClass(response.result.Status)
	}
	if err != nil {
		class := g.errorClass(err)
		outcome := g.responseFailureOutcome(method, response.result.Status)
		response.result.Outcome = outcome
		response.result.RetrySafe = g.canRetryMutation(input, outcome)
		return response, &RequestError{Class: class, Outcome: outcome, cause: err}
	}
	if resp == nil {
		outcome := g.responseFailureOutcome(method, 0)
		response.result.Outcome = outcome
		return response, &RequestError{Class: "network", Outcome: outcome}
	}
	if response.isHTTPError {
		return response, nil
	}
	if len(response.body) > 0 && target != nil {
		if err := json.Unmarshal(response.body, target); err != nil {
			outcome := g.responseFailureOutcome(method, response.result.Status)
			response.result.Outcome = outcome
			response.result.RetrySafe = g.canRetryMutation(input, outcome)
			return response, &RequestError{Class: "decode", Outcome: outcome, cause: err}
		}
	}
	if response.isAccepted && len(response.body) > 0 {
		var receipt struct {
			OperationID string `json:"operation_id"`
		}
		if json.Unmarshal(response.body, &receipt) == nil {
			response.result.ReceiptID = safeRequestID(receipt.OperationID)
		}
	}
	return response, nil
}

func (g *httpGateway) validate(request request) (string, error) {
	method := strings.ToUpper(request.Method)
	if !slices.Contains([]string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete}, method) {
		return "", errors.New("unsupported method")
	}
	if len(request.Body) > defaultMaxResponseBytes {
		return "", errors.New("request body exceeds the 2 mib limit")
	}
	if len(request.Body) > 0 && !jsontext.Value(request.Body).IsValid() {
		return "", errors.New("request body must be valid json")
	}
	if method == http.MethodGet && len(request.Body) > 0 {
		return "", errors.New("get body is not supported")
	}
	if request.IdempotencyKey != "" && (method != http.MethodPost || request.Route != "/sequences" || !validHeaderValue(request.IdempotencyKey, 128)) {
		return "", errors.New("idempotency key is unsupported for this route")
	}
	if request.RequestID != "" && !validHeaderValue(request.RequestID, 128) {
		return "", errors.New("invalid request id")
	}
	if len(queryValues(request.Query).Encode()) > maxQueryBytes {
		return "", errors.New("query exceeds the 4 kib limit")
	}
	if err := validateRoute(request.Route, request.PathParams); err != nil {
		return "", err
	}
	return method, nil
}

func validateRoute(route string, params map[string]string) error {
	if !validRouteTemplate(route) {
		return errors.New("route must be an absolute api path template")
	}
	used := make(map[string]struct{})
	for _, segment := range strings.Split(route[1:], "/") {
		if !strings.HasPrefix(segment, "{") || !strings.HasSuffix(segment, "}") {
			continue
		}
		name := segment[1 : len(segment)-1]
		value, ok := params[name]
		if !ok || value == "" || len(value) > maxPathParamSize || strings.ContainsAny(value, "/\\\x00\r\n") {
			return errors.New("missing or invalid route parameter")
		}
		used[name] = struct{}{}
	}
	if len(used) != len(params) {
		return errors.New("unexpected route parameter")
	}
	return nil
}

func validRouteTemplate(route string) bool {
	if route == "" || len(route) > maxRouteSize || route[0] != '/' || strings.ContainsAny(route, "?#%") {
		return false
	}
	for _, segment := range strings.Split(route[1:], "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if !routeParamPattern.MatchString(segment[1 : len(segment)-1]) {
				return false
			}
		} else if !routeSegmentPattern.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func parseBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || len(raw) > 2048 || !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("ara client: base url must be an absolute http(s) url without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/api/v1") {
		u.Path += "/api/v1"
	}
	u.RawPath = ""
	return u.String(), nil
}

func queryValues(values map[string][]string) url.Values {
	query := make(url.Values, len(values))
	for key, entries := range values {
		query[key] = slices.Clone(entries)
	}
	return query
}

func (g *httpGateway) shouldRetry(request request, response gatewayResponse, err error) bool {
	if !g.isRead(request) {
		return false
	}
	if err != nil {
		var requestError *RequestError
		return errors.As(err, &requestError) && requestError.Class == "network"
	}
	return response.result.Status == http.StatusBadGateway || response.result.Status == http.StatusServiceUnavailable || response.result.Status == http.StatusGatewayTimeout
}

func (g *httpGateway) isRead(request request) bool {
	return strings.EqualFold(request.Method, http.MethodGet)
}

func (g *httpGateway) methodLabel(method string) string {
	method = strings.ToUpper(method)
	if slices.Contains([]string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete}, method) {
		return method
	}
	return "unknown"
}

func (g *httpGateway) routeLabel(route string) string {
	if validRouteTemplate(route) {
		return route
	}
	return "unknown"
}

func (g *httpGateway) httpOutcome(method string, status int) Outcome {
	switch {
	case status == http.StatusAccepted:
		return OutcomeAccepted
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return OutcomeCompleted
	default:
		return g.responseFailureOutcome(method, status)
	}
}

func (g *httpGateway) responseFailureOutcome(method string, status int) Outcome {
	if method != http.MethodGet && (status == 0 || status < http.StatusBadRequest || status >= http.StatusInternalServerError) {
		return OutcomeUnknown
	}
	return OutcomeFailed
}

func (g *httpGateway) canRetryMutation(request request, outcome Outcome) bool {
	return outcome == OutcomeUnknown && strings.EqualFold(request.Method, http.MethodPost) && request.Route == "/sequences" && request.IdempotencyKey != ""
}

// ponytail: bounded linear GET backoff; add Retry-After and jitter if fleet-wide
// retries synchronize.
func (g *httpGateway) retryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * 50 * time.Millisecond
}

func (g *httpGateway) statusClass(status int) string {
	if status < http.StatusContinue {
		return "none"
	}
	return fmt.Sprintf("%dxx", status/100)
}

func (g *httpGateway) errorClass(err error) string {
	switch {
	case errors.Is(err, resty.ErrResponseBodyTooLarge):
		return "response_too_large"
	case errors.Is(err, resty.ErrAutoRedirectDisabled):
		return "redirect"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "network"
	}
}

func parseAPIError(status int, body []byte) *APIError {
	var problem struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &problem); err != nil {
			return &APIError{Status: status}
		}
	}
	return &APIError{Status: status, Type: sanitizeProblemText(problem.Type), Title: sanitizeProblemText(problem.Title), Detail: sanitizeProblemText(problem.Detail)}
}

func sanitizeProblemText(value string) string {
	value = credentialPattern.ReplaceAllString(value, "$1=[redacted]")
	value = bearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = userInfoPattern.ReplaceAllString(value, "$1[redacted]@")
	value = querySecretPattern.ReplaceAllString(value, "$1[redacted]")
	value = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		return r
	}, value)
	if len(value) > maxProblemText {
		value = strings.ToValidUTF8(value[:maxProblemText], "�")
	}
	return strings.TrimSpace(value)
}

func validHeaderValue(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func safeRequestID(value string) string {
	if !validHeaderValue(value, 128) {
		return ""
	}
	return value
}
