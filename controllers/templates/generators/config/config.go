package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	templatesv1 "github.com/weaveworks/gitopssets-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weaveworks/gitopssets-controller/controllers/templates/generators"
)

// ConfigGenerator generates a single resource from a referenced ConfigMap or
// Secret.
type ConfigGenerator struct {
	client.Client
	logr.Logger
}

// GeneratorFactory is a function for creating per-reconciliation generators for
// the ConfigGenerator.
func GeneratorFactory(l logr.Logger, c client.Client) generators.Generator {
	return NewGenerator(l, c)
}

// NewGenerator creates and returns a new config generator.
func NewGenerator(l logr.Logger, c client.Client) *ConfigGenerator {
	return &ConfigGenerator{
		Client: c,
		Logger: l,
	}
}

func (g *ConfigGenerator) Generate(ctx context.Context, sg *templatesv1.GitOpsSetGenerator, ks *templatesv1.GitOpsSet) ([]map[string]any, error) {
	if sg == nil {
		return nil, generators.ErrEmptyGitOpsSet
	}

	if sg.Config == nil {
		return nil, nil
	}

	if sg.Config.Selector == nil && sg.Config.Name == "" {
		return nil, errors.New("name or labelSelector must be provided")
	}

	selector, err := metav1.LabelSelectorAsSelector(sg.Config.Selector)
	if err != nil {
		return nil, err
	}

	g.Logger.Info("generating params from Config generator")

	var paramsList []map[string]any

	switch sg.Config.Kind {
	case "ConfigMap":
		data, err := configMapToParams(ctx, g.Client, client.ObjectKey{Name: sg.Config.Name, Namespace: ks.GetNamespace()}, selector)
		if err != nil {
			return nil, err
		}
		paramsList = data

	case "Secret":
		data, err := secretToParams(ctx, g.Client, client.ObjectKey{Name: sg.Config.Name, Namespace: ks.GetNamespace()}, selector)
		if err != nil {
			return nil, err
		}
		paramsList = data

	default:
		return nil, fmt.Errorf("unknown Config Kind %q %q", sg.Config.Kind, sg.Config.Name)
	}

	return paramsList, nil
}

// Interval is an implementation of the Generator interface.
func (g *ConfigGenerator) Interval(sg *templatesv1.GitOpsSetGenerator) time.Duration {
	return generators.NoRequeueInterval
}

func configMapToParams(ctx context.Context, k8sClient client.Client, key client.ObjectKey, selector labels.Selector) ([]map[string]any, error) {
	data := []map[string]string{}

	if key.Name != "" {
		var configMap corev1.ConfigMap
		if err := k8sClient.Get(ctx, key, &configMap); err != nil {
			return nil, err
		}
		data = append(data, configMap.Data)
	}

	configMaps := corev1.ConfigMapList{}
	if err := k8sClient.List(ctx, &configMaps, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, err
	}

	for i := range configMaps.Items {
		data = append(data, configMaps.Items[i].Data)
	}

	return mapToAnyMaps(data), nil
}

func secretToParams(ctx context.Context, k8sClient client.Client, key client.ObjectKey, selector labels.Selector) ([]map[string]any, error) {
	data := []map[string][]byte{}

	if key.Name != "" {
		var secret corev1.Secret
		if err := k8sClient.Get(ctx, key, &secret); err != nil {
			return nil, err
		}
		data = append(data, secret.Data)
	}

	secrets := corev1.SecretList{}
	if err := k8sClient.List(ctx, &secrets, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, err
	}

	for i := range secrets.Items {
		data = append(data, secrets.Items[i].Data)
	}

	return mapToAnyMaps(data), nil
}

func mapToAnyMaps[V any](maps []map[string]V) []map[string]any {
	result := []map[string]any{}

	for _, m := range maps {
		newMap := map[string]any{}
		for k, v := range m {
			newMap[k] = v
		}
		result = append(result, newMap)
	}

	return result
}
