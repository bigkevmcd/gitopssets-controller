package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	templatesv1 "github.com/weaveworks/gitopssets-controller/api/v1alpha1"
	"github.com/weaveworks/gitopssets-controller/controllers/templates/generators"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GeneratorFactory is a function for creating per-reconciliation generators for
// the MatrixGenerator.
func GeneratorFactory(httpClient *http.Client) generators.GeneratorFactory {
	return func(l logr.Logger, c client.Client) generators.Generator {
		return NewGenerator(l, c, httpClient)
	}
}

// APIClientGenerator generates from an API endpoint.
type APIClientGenerator struct {
	client.Client
	HTTPClient *http.Client
	logr.Logger
}

// NewGenerator creates and returns a new API client generator.
func NewGenerator(l logr.Logger, c client.Client, httpClient *http.Client) *APIClientGenerator {
	return &APIClientGenerator{
		Client:     c,
		Logger:     l,
		HTTPClient: httpClient,
	}
}

// Generate makes an HTTP request using the APIClient definition and returns the
// result converted to a slice of maps.
func (g *APIClientGenerator) Generate(ctx context.Context, sg *templatesv1.GitOpsSetGenerator, gsg *templatesv1.GitOpsSet) ([]map[string]any, error) {
	if sg == nil {
		g.Logger.Info("no generator provided")
		return nil, generators.ErrEmptyGitOpsSet
	}

	if sg.APIClient == nil {
		g.Logger.Info("API client info is nil")
		return nil, nil
	}

	g.Logger.Info("generating params from APIClient generator", "endpoint", sg.APIClient.Endpoint)

	req, err := g.createRequest(ctx, sg.APIClient, gsg.GetNamespace())
	if err != nil {
		g.Logger.Error(err, "failed to create request", "endpoint", sg.APIClient.Endpoint)
		return nil, err
	}

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		g.Logger.Error(err, "failed to fetch endpoint", "endpoint", sg.APIClient.Endpoint)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		g.Logger.Error(err, "failed to read response", "endpoint", sg.APIClient.Endpoint)
		return nil, err
	}

	// Anything 400+ is an error?
	if resp.StatusCode >= http.StatusBadRequest {
		g.Logger.Info("failed to fetch endpoint", "endpoint", sg.APIClient.Endpoint, "statusCode", resp.StatusCode, "response", string(body))
		return nil, fmt.Errorf("got %d response from endpoint %s", resp.StatusCode, sg.APIClient.Endpoint)
	}

	if sg.APIClient.JSONPath == "" {
		if sg.APIClient.SingleElement {
			return g.generateFromResponseBodySingleElement(body, sg.APIClient.Endpoint)
		}
		return g.generateFromResponseBody(body, sg.APIClient.Endpoint)
	}

	return g.generateFromJSONPath(body, sg.APIClient.Endpoint, sg.APIClient.JSONPath)
}

// Interval is an implementation of the Generator interface.
//
// The APIClientGenerator requires to poll regularly as there's nothing to drive
// watches.
func (g *APIClientGenerator) Interval(sg *templatesv1.GitOpsSetGenerator) time.Duration {
	return sg.APIClient.Interval.Duration
}

func (g *APIClientGenerator) createRequest(ctx context.Context, ac *templatesv1.APIClientGenerator, namespace string) (*http.Request, error) {
	method := ac.Method
	if ac.Body != nil {
		method = http.MethodPost
	}

	var body io.Reader
	if ac.Body != nil {
		body = bytes.NewReader(ac.Body.Raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, ac.Endpoint, body)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if ac.HeadersRef != nil {
		if ac.HeadersRef.Kind == "Secret" {
			return req, addHeadersFromSecretToRequest(ctx, g.Client, req, client.ObjectKey{Name: ac.HeadersRef.Name, Namespace: namespace})
		}
		if ac.HeadersRef.Kind == "ConfigMap" {
			return req, addHeadersFromConfigMapToRequest(ctx, g.Client, req, client.ObjectKey{Name: ac.HeadersRef.Name, Namespace: namespace})
		}
	}

	return req, nil
}

func (g *APIClientGenerator) generateFromResponseBody(body []byte, endpoint string) ([]map[string]any, error) {
	var result []map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		g.Logger.Error(err, "failed to unmarshal JSON response", "endpoint", endpoint)
		return nil, fmt.Errorf("failed to unmarshal JSON response from endpoint %s", endpoint)
	}

	res := []map[string]any{}
	for _, v := range result {
		res = append(res, v)
	}

	return res, nil
}

func (g *APIClientGenerator) generateFromResponseBodySingleElement(body []byte, endpoint string) ([]map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		g.Logger.Error(err, "failed to unmarshal JSON response", "endpoint", endpoint)
		return nil, fmt.Errorf("failed to unmarshal JSON response from endpoint %s", endpoint)
	}

	return []map[string]any{result}, nil
}

func (g *APIClientGenerator) generateFromJSONPath(body []byte, endpoint, jsonPath string) ([]map[string]any, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		g.Logger.Error(err, "failed to unmarshal JSON response", "endpoint", endpoint)
		return nil, fmt.Errorf("failed to unmarshal JSON response from endpoint %s", endpoint)
	}

	jp := jsonpath.New("apiclient")
	err := jp.Parse(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSONPath for APIClient generator %q: %w", jsonPath, err)
	}

	results, err := jp.FindResults(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to find results from expression %s accessing endpoint %s: %w", jsonPath, endpoint, err)
	}

	res := []map[string]any{}
	for _, r := range results {
		if l := len(r); l != 1 {
			return nil, fmt.Errorf("%d results found with expression %s", l, jsonPath)
		}

		for _, v := range r {
			items, ok := v.Interface().([]any)
			if !ok {
				return nil, fmt.Errorf("failed to parse response JSONPath %s did not generate suitable array accessing endpoint %s", jsonPath, endpoint)
			}
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("failed to parse response JSONPath %s did not generate suitable values accessing endpoint %s", jsonPath, endpoint)
				}
				res = append(res, item)
			}
		}
	}

	return res, nil
}

func addHeadersFromSecretToRequest(ctx context.Context, k8sClient client.Client, req *http.Request, name client.ObjectKey) error {
	var s corev1.Secret
	if err := k8sClient.Get(ctx, name, &s); err != nil {
		return fmt.Errorf("failed to load Secret for Request headers %s: %w", name, err)
	}

	for k, v := range s.Data {
		req.Header.Set(k, string(v))
	}

	return nil
}

func addHeadersFromConfigMapToRequest(ctx context.Context, k8sClient client.Client, req *http.Request, name client.ObjectKey) error {
	var configMap corev1.ConfigMap
	if err := k8sClient.Get(ctx, name, &configMap); err != nil {
		return fmt.Errorf("failed to load ConfigMap for Request headers %s: %w", name, err)
	}

	for k, v := range configMap.Data {
		req.Header.Set(k, v)
	}

	return nil
}
