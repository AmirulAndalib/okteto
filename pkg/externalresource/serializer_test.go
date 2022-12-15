package externalresource

import (
	"reflect"
	"testing"

	yaml "gopkg.in/yaml.v2"
)

func TestExternalResource_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		expected    ExternalResource
		expectedErr bool
	}{
		{
			name: "invalid external resource: wrong input format",
			data: []byte(`
icon: myicon
notes: /path/to/file
endpoints:
 name: endpoint1
 url: /some/url/1`),
			expectedErr: true,
		},
		{
			name: "invalid external resource: duplicated endpoint names",
			data: []byte(`
icon: myicon
notes: /path/to/file
endpoints:
- name: endpoint1
  url: /some/url/1
- name: endpoint1
  url: /some/url/1`),
			expectedErr: true,
		},
		{
			name: "invalid external resource: no endpoint declared",
			data: []byte(`
icon: myicon
notes: /path/to/file`),
			expectedErr: true,
		},
		{
			name: "invalid external resource: property 'notes' empty",
			data: []byte(`
icon: myicon
endpoints:
- name: endpoint1
  url: /some/url/1`),
			expectedErr: true,
		},
		{
			name: "valid external resource",
			data: []byte(`
icon: myicon
notes: /path/to/file
endpoints:
- name: endpoint1
  url: /some/url/1`),
			expected: ExternalResource{
				Icon: "myicon",
				Notes: Notes{
					Path: "/path/to/file",
				},
				Endpoints: []ExternalEndpoint{
					{
						Name: "endpoint1",
						Url:  "/some/url/1",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result ExternalResource
			if err := yaml.Unmarshal([]byte(tt.data), &result); err != nil {
				if tt.expectedErr {
					return
				}

				t.Fatalf("no error expected but got: %s", err.Error())
			}

			if tt.expectedErr {
				t.Fatal("didn't got expected error")
			}

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("didn't unmarshal correctly. Actual '%+v', Expected '%+v'", result, tt.expected)
			}
		})
	}
}