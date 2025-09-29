package externaldns

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	externaldnsv1alpha1 "sigs.k8s.io/external-dns/apis/v1alpha1"
)

func TestGenerateConsistentSerial(t *testing.T) {
	tests := []struct {
		name           string
		creationTime   *metav1.Time
		generation     int64
		expectedSerial uint32
	}{
		{
			name:           "With creation time 2022-01-16 and generation 1",
			creationTime:   &metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)},
			generation:     1,
			expectedSerial: 20220116*100 + 1, // 2022011601
		},
		{
			name:           "With creation time 2022-01-16 and generation 5",
			creationTime:   &metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)},
			generation:     5,
			expectedSerial: 20220116*100 + 5, // 2022011605
		},
		{
			name:           "With creation time 2022-01-16 and generation 0",
			creationTime:   &metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)},
			generation:     0,
			expectedSerial: 20220116*100 + 0, // 2022011600
		},
		{
			name:           "Different creation time 2025-07-29",
			creationTime:   &metav1.Time{Time: time.Date(2025, 7, 29, 12, 0, 0, 0, time.UTC)},
			generation:     1,
			expectedSerial: 20250729*100 + 1, // 2025072901
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &externaldnsv1alpha1.DNSEndpoint{}
			obj.SetGeneration(tt.generation)
			if tt.creationTime != nil {
				obj.SetCreationTimestamp(*tt.creationTime)
			}

			serial1 := generateConsistentSerial(obj)
			serial2 := generateConsistentSerial(obj)

			// Test consistency - same input should always produce same output
			require.Equal(t, serial1, serial2, "Inconsistent serial generation")
			require.Equal(t, tt.expectedSerial, serial1, "Expected serial to match")
		})
	}
}

func TestGenerateConsistentSerialWithoutCreationTime(t *testing.T) {
	// Test behavior when creation timestamp is missing (should use current time)
	obj := &externaldnsv1alpha1.DNSEndpoint{}
	obj.SetGeneration(2)
	// Don't set creation timestamp

	serial1 := generateConsistentSerial(obj)

	// Should be reasonable (current time in YYYYMMDDnn format + generation)
	now := time.Now()
	year := now.Year()
	month := int(now.Month())
	day := now.Day()
	expectedBase := uint32(year*1000000 + month*10000 + day*100)
	expected := expectedBase + 2

	// Allow some tolerance for date boundary crossing during test execution
	require.GreaterOrEqual(t, serial1, expected-200, "Serial should be within expected range")
	require.LessOrEqual(t, serial1, expected+200, "Serial should be within expected range")
	// Test consistency
	serial2 := generateConsistentSerial(obj)
	require.Equal(t, serial1, serial2, "Consistent serial generation should be deterministic")
}

func TestSerialGenerationDeterministic(t *testing.T) {
	// Test that identical CRs generate identical serials
	creationTime := metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)}

	obj1 := &externaldnsv1alpha1.DNSEndpoint{}
	obj1.SetCreationTimestamp(creationTime)
	obj1.SetGeneration(3)

	obj2 := &externaldnsv1alpha1.DNSEndpoint{}
	obj2.SetCreationTimestamp(creationTime)
	obj2.SetGeneration(3)

	serial1 := generateConsistentSerial(obj1)
	serial2 := generateConsistentSerial(obj2)

	require.Equal(t, serial1, serial2, "Identical CRs should generate identical serials")
	expectedSerial := uint32(20220116*100 + 3) // 2022011603
	require.Equal(t, expectedSerial, serial1, "Expected serial to match")
}

func TestSerialGenerationUnique(t *testing.T) {
	// Test that different CRs generate different serials
	creationTime := metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)}

	// Same creation time, different generation
	obj1 := &externaldnsv1alpha1.DNSEndpoint{}
	obj1.SetCreationTimestamp(creationTime)
	obj1.SetGeneration(1)

	obj2 := &externaldnsv1alpha1.DNSEndpoint{}
	obj2.SetCreationTimestamp(creationTime)
	obj2.SetGeneration(2)

	serial1 := generateConsistentSerial(obj1)
	serial2 := generateConsistentSerial(obj2)

	require.NotEqual(t, serial1, serial2, "Different generations should generate different serials")

	// Different creation time, same generation
	obj3 := &externaldnsv1alpha1.DNSEndpoint{}
	obj3.SetCreationTimestamp(metav1.Time{Time: time.Date(2022, 1, 17, 15, 14, 38, 0, time.UTC)})
	obj3.SetGeneration(1)

	serial3 := generateConsistentSerial(obj3)

	require.NotEqual(t, serial1, serial3, "Different creation times should generate different serials")
}

func TestSerialGenerationConsistencyAcrossInstances(t *testing.T) {
	// Test that the same CR state produces the same serial across different CoreDNS instances
	// This is the main requirement for cluster consistency

	creationTime := metav1.Time{Time: time.Date(2022, 1, 16, 15, 14, 38, 0, time.UTC)}
	generation := int64(5)

	// Simulate the same CR being processed by different CoreDNS instances
	for i := 0; i < 10; i++ {
		obj := &externaldnsv1alpha1.DNSEndpoint{}
		obj.SetCreationTimestamp(creationTime)
		obj.SetGeneration(generation)

		serial := generateConsistentSerial(obj)
		expectedSerial := uint32(20220116*100 + 5) // 2022011605

		require.Equal(t, expectedSerial, serial, "Instance %d: Expected serial to match", i)
	}
}

func TestSerialGenerationOverflow(t *testing.T) {
	// Test behavior with generation values that could overflow the nn part (00-99)
	tests := []struct {
		name         string
		creationTime time.Time
		generation   int64
		expectValid  bool
	}{
		{
			name:         "Normal generation within nn range",
			creationTime: time.Date(2025, 7, 29, 12, 0, 0, 0, time.UTC),
			generation:   50,
			expectValid:  true, // 2025072950
		},
		{
			name:         "Generation at nn limit",
			creationTime: time.Date(2025, 7, 29, 12, 0, 0, 0, time.UTC),
			generation:   99,
			expectValid:  true, // 2025072999
		},
		{
			name:         "Generation exceeding nn range",
			creationTime: time.Date(2025, 7, 29, 12, 0, 0, 0, time.UTC),
			generation:   150,
			expectValid:  true, // Will be 20250729*100 + 150 = 2025072950 + 100 = 2025073050 (wraps around)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &externaldnsv1alpha1.DNSEndpoint{}
			obj.SetCreationTimestamp(metav1.Time{Time: tt.creationTime})
			obj.SetGeneration(tt.generation)

			serial := generateConsistentSerial(obj)

			// Calculate expected value
			year := tt.creationTime.Year()
			month := int(tt.creationTime.Month())
			day := tt.creationTime.Day()
			baseSerial := uint32(year*1000000 + month*10000 + day*100)
			expected := baseSerial + uint32(tt.generation)

			require.Equal(t, expected, serial, "Expected serial to match")
			// Test determinism
			serial2 := generateConsistentSerial(obj)
			require.Equal(t, serial, serial2, "Serial generation should be deterministic")
			t.Logf("CreationTime: %s, Generation: %d -> Serial: %d",
				tt.creationTime.Format("2006-01-02"), tt.generation, serial)
		})
	}
}

func TestSerialGenerationRealistic(t *testing.T) {
	// Test with realistic Kubernetes creation timestamps and generations
	tests := []struct {
		name         string
		creationTime time.Time
		generation   int64
	}{
		{
			name:         "Typical K8s resource",
			creationTime: time.Date(2025, 1, 29, 12, 0, 0, 0, time.UTC),
			generation:   1,
		},
		{
			name:         "Updated resource",
			creationTime: time.Date(2025, 1, 29, 12, 0, 0, 0, time.UTC),
			generation:   10,
		},
		{
			name:         "Heavily updated resource",
			creationTime: time.Date(2025, 1, 29, 12, 0, 0, 0, time.UTC),
			generation:   50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &externaldnsv1alpha1.DNSEndpoint{}
			obj.SetCreationTimestamp(metav1.Time{Time: tt.creationTime})
			obj.SetGeneration(tt.generation)

			serial := generateConsistentSerial(obj)

			// Calculate expected value in YYYYMMDDnn format
			year := tt.creationTime.Year()
			month := int(tt.creationTime.Month())
			day := tt.creationTime.Day()
			baseSerial := uint32(year*1000000 + month*10000 + day*100)
			expected := baseSerial + uint32(tt.generation)

			require.Equal(t, expected, serial, "Expected serial to match")
			// Verify the serial is reasonable for DNS (not zero, within uint32)
			require.NotEqual(t, 0, serial, "Serial should not be zero")
			t.Logf("CreationTime: %s, Generation: %d -> Serial: %d",
				tt.creationTime.Format("2006-01-02"), tt.generation, serial)
		})
	}
}
