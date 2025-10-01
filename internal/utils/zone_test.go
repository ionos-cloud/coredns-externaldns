package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractZoneFromDomain(t *testing.T) {
	tests := []struct {
		name         string
		domain       string
		expectedZone string
	}{
		{
			name:         "Wildcard foo case - the original issue",
			domain:       "*.foo.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "Regular foo subdomain",
			domain:       "foo.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "API in staging",
			domain:       "api.stg.test.com",
			expectedZone: "stg.test.com.",
		},
		{
			name:         "Traditional 2-level domain",
			domain:       "example.com",
			expectedZone: "com.",
		},
		{
			name:         "Subdomain of 2-level domain",
			domain:       "www.example.com",
			expectedZone: "example.com.",
		},
		{
			name:         "Wildcard for 2-level zone",
			domain:       "*.example.com",
			expectedZone: "com.",
		},
		{
			name:         "Deep nesting",
			domain:       "service.api.stg.test.com",
			expectedZone: "api.stg.test.com.",
		},
		{
			name:         "Wildcard deep nesting",
			domain:       "*.service.api.stg.test.com",
			expectedZone: "api.stg.test.com.",
		},
		{
			name:         "Single domain",
			domain:       "localhost",
			expectedZone: "localhost.",
		},
		{
			name:         "Empty domain",
			domain:       "",
			expectedZone: ".",
		},
		{
			name:         "Case normalization",
			domain:       "*.foo.STG.test.com",
			expectedZone: "stg.test.com.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			zone := ExtractZoneFromDomain(test.domain)
			assert.Equal(t, test.expectedZone, zone,
				"For domain %s, expected zone %s but got %s",
				test.domain, test.expectedZone, zone)
		})
	}
}

func TestExtractZoneFromDomainEdgeCases(t *testing.T) {
	t.Run("Multiple wildcards", func(t *testing.T) {
		// Should only remove the first *., then process the remaining domain
		// *.*.example.com -> *.example.com (3 parts) -> example.com. (zone)
		domain := "*.*.example.com"
		expected := "example.com."
		actual := ExtractZoneFromDomain(domain)
		assert.Equal(t, expected, actual)
	})

	t.Run("Wildcard without subdomain", func(t *testing.T) {
		domain := "*.com"
		expected := "com."
		actual := ExtractZoneFromDomain(domain)
		assert.Equal(t, expected, actual)
	})

	t.Run("Domain already normalized", func(t *testing.T) {
		domain := "www.example.com."
		expected := "example.com."
		actual := ExtractZoneFromDomain(domain)
		assert.Equal(t, expected, actual)
	})
}
