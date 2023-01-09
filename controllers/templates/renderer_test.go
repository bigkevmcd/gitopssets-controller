package templates

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	templatesv1 "github.com/weaveworks/gitopssets-controller/api/v1alpha1"
	"github.com/weaveworks/gitopssets-controller/controllers/templates/generators"
	"github.com/weaveworks/gitopssets-controller/controllers/templates/generators/list"
	"github.com/weaveworks/gitopssets-controller/test"
)

const (
	testGitOpsSetName      = "test-gitops-set"
	testGitOpsSetNamespace = "demo"
)

func TestRender(t *testing.T) {
	testGenerators := map[string]generators.Generator{
		"List": list.NewGenerator(logr.Discard()),
	}

	generatorTests := []struct {
		name       string
		elements   []apiextensionsv1.JSON
		setOptions []func(*templatesv1.GitOpsSet)
		want       []runtime.Object
	}{
		{
			name: "multiple elements",
			elements: []apiextensionsv1.JSON{
				{Raw: []byte(`{"env": "engineering-dev","externalIP": "192.168.50.50"}`)},
				{Raw: []byte(`{"env": "engineering-prod","externalIP": "192.168.100.20"}`)},
				{Raw: []byte(`{"env": "engineering-preprod","externalIP": "192.168.150.30"}`)},
			},
			want: []runtime.Object{
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-dev-demo"), setClusterIP("192.168.50.50"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-dev")}))),
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-prod-demo"), setClusterIP("192.168.100.20"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-prod")}))),
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-preprod-demo"), setClusterIP("192.168.150.30"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-preprod")}))),
			},
		},
		{
			name: "sanitization",
			elements: []apiextensionsv1.JSON{
				{Raw: []byte(`{"env": "engineering dev","externalIP": "192.168.50.50"}`)},
			},
			setOptions: []func(*templatesv1.GitOpsSet){
				func(s *templatesv1.GitOpsSet) {
					s.Spec.Templates = []templatesv1.GitOpsSetTemplate{
						{
							runtime.RawExtension{
								Raw: mustMarshalYAML(t, makeTestService(types.NamespacedName{Name: "{{sanitize .env}}-demo"})),
							},
						},
					}
				},
			},
			want: []runtime.Object{
				newTestUnstructured(t, makeTestService(nsn("demo", "engineeringdev-demo"),
					setClusterIP("192.168.50.50"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering dev")}))),
			},
		},
		{
			name: "multiple templates yields cartesian result",
			elements: []apiextensionsv1.JSON{
				{Raw: []byte(`{"env": "engineering-dev","externalIP": "192.168.50.50"}`)},
				{Raw: []byte(`{"env": "engineering-prod","externalIP": "192.168.100.20"}`)},
			},
			setOptions: []func(*templatesv1.GitOpsSet){
				func(s *templatesv1.GitOpsSet) {
					s.Spec.Templates = []templatesv1.GitOpsSetTemplate{
						{
							runtime.RawExtension{
								Raw: mustMarshalYAML(t, makeTestService(types.NamespacedName{Name: "{{ .env}}-demo1"})),
							},
						},
						{
							runtime.RawExtension{
								Raw: mustMarshalYAML(t, makeTestService(types.NamespacedName{Name: "{{ .env}}-demo2"})),
							},
						},
					}
				},
			},
			want: []runtime.Object{
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-dev-demo1"), setClusterIP("192.168.50.50"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-dev")}))),
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-dev-demo2"), setClusterIP("192.168.50.50"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-dev")}))),
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-prod-demo1"), setClusterIP("192.168.100.20"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-prod")}))),
				newTestUnstructured(t, makeTestService(nsn("demo", "engineering-prod-demo2"), setClusterIP("192.168.100.20"),
					addAnnotations(map[string]string{"app.kubernetes.io/instance": string("engineering-prod")}))),
			},
		},
	}

	for _, tt := range generatorTests {
		t.Run(tt.name, func(t *testing.T) {
			gset := makeTestGitOpsSet(t, append(tt.setOptions, listElements(tt.elements))...)
			objs, err := Render(context.TODO(), gset, testGenerators)
			test.AssertNoError(t, err)

			if diff := cmp.Diff(tt.want, objs); diff != "" {
				t.Fatalf("failed to generate resources:\n%s", diff)
			}
		})
	}
}

func TestRender_errors(t *testing.T) {
	templateTests := []struct {
		name       string
		setOptions []func(*templatesv1.GitOpsSet)
		wantErr    string
	}{
		{
			name: "bad template",
			setOptions: []func(*templatesv1.GitOpsSet){
				func(s *templatesv1.GitOpsSet) {
					s.Spec.Templates = []templatesv1.GitOpsSetTemplate{
						{
							runtime.RawExtension{
								Raw: mustMarshalYAML(t, makeTestService(types.NamespacedName{Name: "{{ .unknown}}-demo1"})),
							},
						},
					}
				},
			},
			wantErr: "template is empty",
		},
	}

	testGenerators := map[string]generators.Generator{
		"List": list.NewGenerator(logr.Discard()),
	}

	for _, tt := range templateTests {
		t.Run(tt.name, func(t *testing.T) {
			gset := makeTestGitOpsSet(t, tt.setOptions...)
			_, err := Render(context.TODO(), gset, testGenerators)

			test.AssertErrorMatch(t, tt.wantErr, err)
		})
	}
}

func listElements(el []apiextensionsv1.JSON) func(*templatesv1.GitOpsSet) {
	return func(gs *templatesv1.GitOpsSet) {
		if gs.Spec.Generators == nil {
			gs.Spec.Generators = []templatesv1.GitOpsSetGenerator{}
		}
		gs.Spec.Generators = append(gs.Spec.Generators,
			templatesv1.GitOpsSetGenerator{
				List: &templatesv1.ListGenerator{
					Elements: el,
				},
			})
	}
}

func makeTestGitOpsSet(t *testing.T, opts ...func(*templatesv1.GitOpsSet)) *templatesv1.GitOpsSet {
	ks := &templatesv1.GitOpsSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGitOpsSetName,
			Namespace: testGitOpsSetNamespace,
		},
		Spec: templatesv1.GitOpsSetSpec{
			Templates: []templatesv1.GitOpsSetTemplate{
				{
					runtime.RawExtension{
						Raw: mustMarshalYAML(t, makeTestService(types.NamespacedName{Name: "{{.env}}-demo"})),
					},
				},
			},
		},
	}
	for _, o := range opts {
		o(ks)
	}

	return ks
}

func makeTestService(name types.NamespacedName, opts ...func(*corev1.Service)) *corev1.Service {
	s := corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Annotations: map[string]string{
				"app.kubernetes.io/instance": "{{ .env }}",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "{{ .externalIP }}",
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Protocol:   corev1.ProtocolTCP,
					Port:       8080,
					TargetPort: intstr.FromInt(8080)},
			},
		},
	}
	for _, o := range opts {
		o(&s)
	}

	return &s
}

func setClusterIP(ip string) func(s *corev1.Service) {
	return func(s *corev1.Service) {
		s.Spec.ClusterIP = ip
	}
}

func mustMarshalYAML(t *testing.T, r runtime.Object) []byte {
	b, err := yaml.Marshal(r)
	test.AssertNoError(t, err)

	return b
}

func addAnnotations(ann map[string]string) func(*corev1.Service) {
	return func(s *corev1.Service) {
		s.SetAnnotations(ann)
	}
}

func nsn(namespace, name string) types.NamespacedName {
	return types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
}

func newTestUnstructured(t *testing.T, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	delete(raw, "status")

	return &unstructured.Unstructured{Object: raw}
}
