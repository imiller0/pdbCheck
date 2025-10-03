/****************************************************************
 *
 * This code was developed with AI code generation assistance
 *
 ****************************************************************/
package main

import (
	"testing"
	"time"

	"github.com/imiller0/redhat-utils/pdbCheck/internal/nodegroup"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestPDBViolationWithTwoNodeGroups(t *testing.T) {
	// Test scenario:
	// - 2 node groups
	// - PDB covers 3 pods total
	// - PDB has maxUnavailable of 1
	// - 2 pods in first node group (should violate)
	// - 1 pod in second node group (should not violate)

	// Setup test data
	pdbName := "test-pdb"
	pdbNamespace := "default"

	// PDB configuration: maxUnavailable = 1
	maxUnavailable := intstr.FromInt(1)

	// Create node groups collection
	nodeGroups := nodegroup.NewCollection()

	// First node group with 2 nodes
	nodeGroup1 := nodegroup.New()
	nodeGroup1.Name = "worker"
	nodeGroup1.Nodes["node1"] = true
	nodeGroup1.Nodes["node2"] = true
	nodeGroups.AddGroup(nodeGroup1)

	// Second node group with 1 node
	nodeGroup2 := nodegroup.New()
	nodeGroup2.Name = "master"
	nodeGroup2.Nodes["node3"] = true
	nodeGroups.AddGroup(nodeGroup2)

	// Pod to node mapping (3 pods total)
	podToNode := map[string]string{
		"app-pod-1": "node1", // In worker group
		"app-pod-2": "node2", // In worker group
		"app-pod-3": "node3", // In master group
	}

	// Call the function under test
	violations, err := checkPDBPods(pdbName, pdbNamespace, nil, &maxUnavailable, podToNode, nodeGroups)

	// Verify results
	if err != nil {
		t.Fatalf("checkPDBPods returned unexpected error: %v", err)
	}

	// Should have exactly 1 violation (from worker group)
	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	violation := violations[0]

	// Verify violation details
	if violation.PDBName != pdbName {
		t.Errorf("Expected PDBName %s, got %s", pdbName, violation.PDBName)
	}

	if violation.PDBNamespace != pdbNamespace {
		t.Errorf("Expected PDBNamespace %s, got %s", pdbNamespace, violation.PDBNamespace)
	}

	if violation.NodeGroupName != "worker" {
		t.Errorf("Expected NodeGroupName 'worker', got %s", violation.NodeGroupName)
	}

	if violation.PodCount != 3 {
		t.Errorf("Expected PodCount 3, got %d", violation.PodCount)
	}

	if violation.MaxDisruptable != 1 {
		t.Errorf("Expected MaxDisruptable 1, got %d", violation.MaxDisruptable)
	}

	if violation.PodsInGroup != 2 {
		t.Errorf("Expected PodsInGroup 2, got %d", violation.PodsInGroup)
	}

	// Verify the pods in the violating node group
	expectedPods := []string{"app-pod-1", "app-pod-2"}
	if len(violation.PodsInNodeGroup) != 2 {
		t.Errorf("Expected 2 pods in NodeGroup, got %d", len(violation.PodsInNodeGroup))
	}

	// Check that both expected pods are present
	podMap := make(map[string]bool)
	for _, pod := range violation.PodsInNodeGroup {
		podMap[pod] = true
	}

	for _, expectedPod := range expectedPods {
		if !podMap[expectedPod] {
			t.Errorf("Expected pod %s not found in PodsInNodeGroup", expectedPod)
		}
	}

	// Verify PDB configuration is captured
	if violation.MaxUnavailable == nil {
		t.Error("Expected MaxUnavailable to be set")
	} else if violation.MaxUnavailable.IntVal != 1 {
		t.Errorf("Expected MaxUnavailable IntVal 1, got %d", violation.MaxUnavailable.IntVal)
	}

	if violation.MinAvailable != nil {
		t.Error("Expected MinAvailable to be nil")
	}
}

func TestPDBViolationWithMinAvailable(t *testing.T) {
	// Test scenario with minAvailable instead of maxUnavailable
	// - 1 node group
	// - PDB covers 4 pods total
	// - PDB has minAvailable of 3
	// - All 4 pods in one node group (should violate since maxDisruptable = 4-3 = 1, but group has 4 > 1)

	pdbName := "test-pdb-min"
	pdbNamespace := "default"

	// PDB configuration: minAvailable = 3 (so maxDisruptable = 4-3 = 1)
	minAvailable := intstr.FromInt(3)

	// Create node group collection
	nodeGroups := nodegroup.NewCollection()
	nodeGroup1 := nodegroup.New()
	nodeGroup1.Name = "worker"
	nodeGroup1.Nodes["node1"] = true
	nodeGroup1.Nodes["node2"] = true
	nodeGroup1.Nodes["node3"] = true
	nodeGroup1.Nodes["node4"] = true
	nodeGroups.AddGroup(nodeGroup1)

	// Pod to node mapping (4 pods total, all in worker group)
	podToNode := map[string]string{
		"app-pod-1": "node1",
		"app-pod-2": "node2",
		"app-pod-3": "node3",
		"app-pod-4": "node4",
	}

	// Call the function under test
	violations, err := checkPDBPods(pdbName, pdbNamespace, &minAvailable, nil, podToNode, nodeGroups)

	// Verify results
	if err != nil {
		t.Fatalf("checkPDBPods returned unexpected error: %v", err)
	}

	// Should have exactly 1 violation (from worker group)
	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}

	violation := violations[0]

	// Verify key details
	if violation.NodeGroupName != "worker" {
		t.Errorf("Expected NodeGroupName 'worker', got %s", violation.NodeGroupName)
	}

	if violation.PodCount != 4 {
		t.Errorf("Expected PodCount 4, got %d", violation.PodCount)
	}

	if violation.MaxDisruptable != 1 {
		t.Errorf("Expected MaxDisruptable 1, got %d", violation.MaxDisruptable)
	}

	if violation.PodsInGroup != 4 {
		t.Errorf("Expected PodsInGroup 4, got %d", violation.PodsInGroup)
	}

	// Verify MinAvailable is captured
	if violation.MinAvailable == nil {
		t.Error("Expected MinAvailable to be set")
	} else if violation.MinAvailable.IntVal != 3 {
		t.Errorf("Expected MinAvailable IntVal 3, got %d", violation.MinAvailable.IntVal)
	}
}

func TestPDBNoViolations(t *testing.T) {
	// Test scenario where no violations occur
	// - 2 node groups
	// - PDB covers 4 pods total
	// - PDB has maxUnavailable of 2
	// - 2 pods in first node group (equals maxDisruptable, no violation)
	// - 2 pods in second node group (equals maxDisruptable, no violation)

	pdbName := "test-pdb-no-violations"
	pdbNamespace := "default"

	// PDB configuration: maxUnavailable = 2
	maxUnavailable := intstr.FromInt(2)

	// Create node groups
	nodeGroups := nodegroup.NewCollection()

	nodeGroup1 := nodegroup.New()
	nodeGroup1.Name = "worker"
	nodeGroup1.Nodes["node1"] = true
	nodeGroup1.Nodes["node2"] = true
	nodeGroups.AddGroup(nodeGroup1)

	nodeGroup2 := nodegroup.New()
	nodeGroup2.Name = "master"
	nodeGroup2.Nodes["node3"] = true
	nodeGroup2.Nodes["node4"] = true
	nodeGroups.AddGroup(nodeGroup2)

	// Pod to node mapping (4 pods total, 2 per group)
	podToNode := map[string]string{
		"app-pod-1": "node1", // In worker group
		"app-pod-2": "node2", // In worker group
		"app-pod-3": "node3", // In master group
		"app-pod-4": "node4", // In master group
	}

	// Call the function under test
	violations, err := checkPDBPods(pdbName, pdbNamespace, nil, &maxUnavailable, podToNode, nodeGroups)

	// Verify results
	if err != nil {
		t.Fatalf("checkPDBPods returned unexpected error: %v", err)
	}

	// Should have no violations
	if len(violations) != 0 {
		t.Fatalf("Expected 0 violations, got %d", len(violations))
	}
}

func TestPDBViolationWithSerialUpgradeTrue(t *testing.T) {
	// Test scenario where SerialUpgrade = true and isVerbose = false
	// Should NOT track violations even when PDB would be violated

	// Temporarily set isVerbose to false (it's a global variable)
	originalVerbose := isVerbose
	isVerbose = false
	defer func() { isVerbose = originalVerbose }()

	pdbName := "test-pdb-serial"
	pdbNamespace := "default"

	// PDB configuration: maxUnavailable = 1
	maxUnavailable := intstr.FromInt(1)

	// Create node groups
	nodeGroups := nodegroup.NewCollection()

	// First node group with SerialUpgrade = true
	nodeGroup1 := nodegroup.New()
	nodeGroup1.Name = "worker"
	nodeGroup1.SerialUpgrade = true // This should prevent violation tracking
	nodeGroup1.Nodes["node1"] = true
	nodeGroup1.Nodes["node2"] = true
	nodeGroups.AddGroup(nodeGroup1)

	// Second node group with SerialUpgrade = false
	nodeGroup2 := nodegroup.New()
	nodeGroup2.Name = "master"
	nodeGroup2.SerialUpgrade = false
	nodeGroup2.Nodes["node3"] = true
	nodeGroups.AddGroup(nodeGroup2)

	// Pod to node mapping (3 pods total)
	podToNode := map[string]string{
		"app-pod-1": "node1", // In worker group (SerialUpgrade=true)
		"app-pod-2": "node2", // In worker group (SerialUpgrade=true)
		"app-pod-3": "node3", // In master group (SerialUpgrade=false)
	}

	// Call the function under test
	violations, err := checkPDBPods(pdbName, pdbNamespace, nil, &maxUnavailable, podToNode, nodeGroups)

	// Verify results
	if err != nil {
		t.Fatalf("checkPDBPods returned unexpected error: %v", err)
	}

	// Should have no violations because worker group has SerialUpgrade=true
	// Even though worker group would violate (2 pods > 1 maxDisruptable)
	if len(violations) != 0 {
		t.Fatalf("Expected 0 violations due to SerialUpgrade=true, got %d", len(violations))
	}
}

func TestCalculatePDBDisruptionLimits(t *testing.T) {
	tests := []struct {
		name           string
		minAvailable   *intstr.IntOrString
		maxUnavailable *intstr.IntOrString
		totalPods      int32
		expectedNeeded int32
		expectedMax    int32
		shouldError    bool
	}{
		{
			name:           "maxUnavailable only",
			minAvailable:   nil,
			maxUnavailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
			totalPods:      5,
			expectedNeeded: 4, // 5 - 1
			expectedMax:    1, // 5 - 4
			shouldError:    false,
		},
		{
			name:           "minAvailable only",
			minAvailable:   &intstr.IntOrString{Type: intstr.Int, IntVal: 3},
			maxUnavailable: nil,
			totalPods:      5,
			expectedNeeded: 3,
			expectedMax:    2, // 5 - 3
			shouldError:    false,
		},
		{
			name:           "both constraints - minAvailable wins",
			minAvailable:   &intstr.IntOrString{Type: intstr.Int, IntVal: 4},
			maxUnavailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 2},
			totalPods:      5,
			expectedNeeded: 4, // max(4, 5-2=3) = 4
			expectedMax:    1, // 5 - 4
			shouldError:    false,
		},
		{
			name:           "both constraints - maxUnavailable wins",
			minAvailable:   &intstr.IntOrString{Type: intstr.Int, IntVal: 2},
			maxUnavailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
			totalPods:      5,
			expectedNeeded: 4, // max(2, 5-1=4) = 4
			expectedMax:    1, // 5 - 4
			shouldError:    false,
		},
		{
			name:           "no constraints",
			minAvailable:   nil,
			maxUnavailable: nil,
			totalPods:      5,
			expectedNeeded: 0,
			expectedMax:    5, // 5 - 0
			shouldError:    false,
		},
		{
			name:           "percentage maxUnavailable",
			minAvailable:   nil,
			maxUnavailable: &intstr.IntOrString{Type: intstr.String, StrVal: "20%"},
			totalPods:      5,
			expectedNeeded: 4, // 5 - ceil(5 * 0.20) = 5 - 1 = 4
			expectedMax:    1, // 5 - 4
			shouldError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			needed, maxDisruptable, err := calculatePDBDisruptionLimits(tt.minAvailable, tt.maxUnavailable, tt.totalPods)

			if tt.shouldError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if needed != tt.expectedNeeded {
				t.Errorf("Expected needed=%d, got %d", tt.expectedNeeded, needed)
			}

			if maxDisruptable != tt.expectedMax {
				t.Errorf("Expected maxDisruptable=%d, got %d", tt.expectedMax, maxDisruptable)
			}
		})
	}
}

func TestPodCache(t *testing.T) {
	cache := NewPodCache()

	// Test adding and retrieving pods
	pod1 := PodInfo{
		Name:              "test-pod-1",
		Namespace:         "default",
		CreationTimestamp: time.Now(),
		NodeName:          "node1",
	}

	pod2 := PodInfo{
		Name:              "test-pod-2",
		Namespace:         "kube-system",
		CreationTimestamp: time.Now().Add(-1 * time.Hour),
		NodeName:          "node2",
	}

	// Add pods to cache
	cache.AddPod(pod1)
	cache.AddPod(pod2)

	// Test GetPod
	retrievedPod1, exists := cache.GetPod("default", "test-pod-1")
	if !exists {
		t.Errorf("Expected to find pod1 in cache")
	}
	if retrievedPod1.Name != pod1.Name || retrievedPod1.Namespace != pod1.Namespace {
		t.Errorf("Retrieved pod1 doesn't match original")
	}

	// Test non-existent pod
	_, exists = cache.GetPod("default", "non-existent")
	if exists {
		t.Errorf("Should not find non-existent pod")
	}

	// Test GetPodsInNamespace
	defaultPods := cache.GetPodsInNamespace("default")
	if len(defaultPods) != 1 {
		t.Errorf("Expected 1 pod in default namespace, got %d", len(defaultPods))
	}
	if defaultPods[0].Name != "test-pod-1" {
		t.Errorf("Expected test-pod-1, got %s", defaultPods[0].Name)
	}

	kubeSystemPods := cache.GetPodsInNamespace("kube-system")
	if len(kubeSystemPods) != 1 {
		t.Errorf("Expected 1 pod in kube-system namespace, got %d", len(kubeSystemPods))
	}
}

func TestPDBViolationWithSerialUpgradeTrueAndAll(t *testing.T) {
	// Test scenario where SerialUpgrade = true but includeAll = true
	// Should track violations because -all flag overrides SerialUpgrade

	// Temporarily set includeAll to true
	originalIncludeAll := includeAll
	includeAll = true
	defer func() { includeAll = originalIncludeAll }()

	pdbName := "test-pdb-serial-all"
	pdbNamespace := "default"

	// PDB configuration: maxUnavailable = 1
	maxUnavailable := intstr.FromInt(1)

	// Create node groups
	nodeGroups := nodegroup.NewCollection()

	// Node group with SerialUpgrade = true
	nodeGroup1 := nodegroup.New()
	nodeGroup1.Name = "worker"
	nodeGroup1.SerialUpgrade = true // Would normally prevent tracking
	nodeGroup1.Nodes["node1"] = true
	nodeGroup1.Nodes["node2"] = true
	nodeGroups.AddGroup(nodeGroup1)

	// Pod to node mapping (2 pods total, both in worker group)
	podToNode := map[string]string{
		"app-pod-1": "node1", // In worker group
		"app-pod-2": "node2", // In worker group
	}

	// Call the function under test
	violations, err := checkPDBPods(pdbName, pdbNamespace, nil, &maxUnavailable, podToNode, nodeGroups)

	// Verify results
	if err != nil {
		t.Fatalf("checkPDBPods returned unexpected error: %v", err)
	}

	// Should have 1 violation because includeAll=true overrides SerialUpgrade=true
	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation due to includeAll=true, got %d", len(violations))
	}

	violation := violations[0]

	// Verify violation details
	if violation.NodeGroupName != "worker" {
		t.Errorf("Expected NodeGroupName 'worker', got %s", violation.NodeGroupName)
	}

	if violation.PodsInGroup != 2 {
		t.Errorf("Expected PodsInGroup 2, got %d", violation.PodsInGroup)
	}

	if violation.MaxDisruptable != 1 {
		t.Errorf("Expected MaxDisruptable 1, got %d", violation.MaxDisruptable)
	}
}
