/****************************************************************
 *
 * This code was developed with AI code generation assistance
 *
 ****************************************************************/

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/imiller0/redhat-utils/pdbCheck/internal/nodegroup"
	machineconfigv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	machineconfigclient "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Global configuration flags
var (
	isVerbose  bool // Enable verbose output
	includeAll bool // Include serial upgrade MCPs in analysis
)

// PDBViolation represents a PodDisruptionBudget violation caused by a NodeGroup
type PDBViolation struct {
	PDBName         string
	PDBNamespace    string
	NodeGroupName   string
	PodsInNodeGroup []string
	PodCount        int32
	MaxDisruptable  int32
	PodsInGroup     int
	MinAvailable    *intstr.IntOrString
	MaxUnavailable  *intstr.IntOrString
}

// ID returns a formatted identifier for the PDB
func (v PDBViolation) ID() string {
	return fmt.Sprintf("%s/%s", v.PDBNamespace, v.PDBName)
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)
}

// groupNodesByMCP creates node groups based on Machine Config Pools
func groupNodesByMCP(tconfig *rest.Config, kubeClient *kubernetes.Clientset) (*nodegroup.Collection, error) {
	mcClient := machineconfigclient.NewForConfigOrDie(tconfig)

	// Fetch MCPs and nodes concurrently
	mcpList, allNodes, err := fetchMCPsAndNodes(mcClient, kubeClient)
	if err != nil {
		return nil, err
	}

	if len(mcpList.Items) == 0 {
		log.Info("No MCPs found")
		return nodegroup.NewCollection(), nil
	}

	// Create node lookup map for efficiency
	nodeMap := createNodeMap(allNodes)

	// Build node groups using the nodegroup package
	return nodegroup.BuildFromMCPs(mcpList, nodeMap)
}

// fetchMCPsAndNodes retrieves MCPs and nodes concurrently
func fetchMCPsAndNodes(mcClient *machineconfigclient.Clientset, kubeClient *kubernetes.Clientset) (*machineconfigv1.MachineConfigPoolList, *corev1.NodeList, error) {
	var mcpList *machineconfigv1.MachineConfigPoolList
	var nodeList *corev1.NodeList
	var mcpErr, nodeErr error

	// Use a wait group for cleaner concurrency
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		mcpList, mcpErr = mcClient.MachineconfigurationV1().MachineConfigPools().List(context.TODO(), metav1.ListOptions{})
	}()

	go func() {
		defer wg.Done()
		nodeList, nodeErr = kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	}()

	wg.Wait()

	if mcpErr != nil {
		return nil, nil, fmt.Errorf("failed to retrieve MCPs: %w", mcpErr)
	}
	if nodeErr != nil {
		return nil, nil, fmt.Errorf("failed to retrieve nodes: %w", nodeErr)
	}

	return mcpList, nodeList, nil
}

// createNodeMap builds a map for efficient node lookups
func createNodeMap(nodeList *corev1.NodeList) map[string]*corev1.Node {
	nodeMap := make(map[string]*corev1.Node, len(nodeList.Items))
	for i := range nodeList.Items {
		nodeMap[nodeList.Items[i].Name] = &nodeList.Items[i]
	}
	return nodeMap
}

func getAsCount(value intstr.IntOrString, count int32) (int32, error) {
	switch value.Type {
	case intstr.Int:
		return value.IntVal, nil
	case intstr.String: // Handle as percent
		trunc := strings.TrimSuffix(value.StrVal, "%")
		pctFloat, err := strconv.ParseFloat(trunc, 32)
		if err != nil {
			return -1, fmt.Errorf("ffailed to convert value %s to percentage: %v", value.StrVal, err)
		}
		return int32(math.Ceil(pctFloat / 100.0 * float64(count))), nil
	}
	// Unreachable...
	return -1, nil
}

// calculatePDBDisruptionLimits calculates how many pods are needed and how many can be disrupted
// based on PDB's minAvailable and maxUnavailable settings
func calculatePDBDisruptionLimits(minAvailable, maxUnavailable *intstr.IntOrString, totalPodCount int32) (needed int32, maxDisruptable int32, err error) {
	needed = int32(0)

	// Calculate minimum needed based on minAvailable
	if minAvailable != nil {
		min, err := getAsCount(*minAvailable, totalPodCount)
		if err != nil {
			return 0, 0, fmt.Errorf("error calculating minAvailable: %w", err)
		}
		if min > needed {
			needed = min
		}
	}

	// Calculate minimum needed based on maxUnavailable
	if maxUnavailable != nil {
		max, err := getAsCount(*maxUnavailable, totalPodCount)
		if err != nil {
			return 0, 0, fmt.Errorf("error calculating maxUnavailable: %w", err)
		}
		// We need totalPodCount - maxUnavailable
		minNeeded := totalPodCount - max
		if minNeeded > needed {
			needed = minNeeded
		}
	}

	maxDisruptable = totalPodCount - needed
	return needed, maxDisruptable, nil
}

/**
* Check if the PDB would be violated if any of the node groups are taken offline
* Returns a slice of PDBViolation for groups that would violate the PDB
 */
func checkPDBPods(pdbName, pdbNamespace string, minAvail, maxUnavail *intstr.IntOrString, podToNode map[string]string, nodeGroups *nodegroup.Collection) ([]PDBViolation, error) {
	podCount := int32(len(podToNode))
	if podCount == 0 {
		log.Warnf("PDB %s/%s has no matching pods", pdbNamespace, pdbName)
		return nil, nil
	}

	needed, maxDisruptable, err := calculatePDBDisruptionLimits(minAvail, maxUnavail, podCount)
	if err != nil {
		return nil, err
	}

	log.Debugf("PDB %s/%s: need %d pods, can disrupt %d", pdbNamespace, pdbName, needed, maxDisruptable)
	if podCount <= needed {
		log.Warnf("PDB %s/%s: no disruptions allowed (%d pods <= %d needed)", pdbNamespace, pdbName, podCount, needed)
	}

	// Group pods by node group
	groupPods := groupPodsByNodeGroup(podToNode, nodeGroups)

	// Check for violations
	return findViolations(pdbName, pdbNamespace, groupPods, maxDisruptable, podCount, minAvail, maxUnavail, nodeGroups), nil
}

// groupPodsByNodeGroup organizes pods by their node groups
func groupPodsByNodeGroup(podToNode map[string]string, nodeGroups *nodegroup.Collection) map[string][]string {
	groupPods := make(map[string][]string)

	for podName, nodeName := range podToNode {
		groupName, exists := nodeGroups.NodeToGroup[nodeName]
		if !exists {
			log.Infof("Pod %s on node %s not in any indexed node group", podName, nodeName)
			groupName = "unknown"
		}
		groupPods[groupName] = append(groupPods[groupName], podName)
	}

	return groupPods
}

// findViolations checks each node group for PDB violations
func findViolations(pdbName, pdbNamespace string, groupPods map[string][]string, maxDisruptable, podCount int32, minAvail, maxUnavail *intstr.IntOrString, nodeGroups *nodegroup.Collection) []PDBViolation {
	var violations []PDBViolation

	for groupName, pods := range groupPods {
		podsInGroup := len(pods)
		if podsInGroup <= int(maxDisruptable) {
			continue // No violation
		}

		// Check if we should include this violation using the nodegroup method
		nodeGroup, exists := nodeGroups.GetGroup(groupName)
		if !exists {
			log.Warnf("NodeGroup %s not found", groupName)
			// Include unknown groups
		} else if !nodeGroup.ShouldIncludeInAnalysis(includeAll) {
			continue // Skip this violation
		}

		log.Debugf("Violation: Group %s has %d pods (max disruptable: %d)", groupName, podsInGroup, maxDisruptable)

		violations = append(violations, PDBViolation{
			PDBName:         pdbName,
			PDBNamespace:    pdbNamespace,
			NodeGroupName:   groupName,
			PodsInNodeGroup: pods,
			PodCount:        podCount,
			MaxDisruptable:  maxDisruptable,
			PodsInGroup:     podsInGroup,
			MinAvailable:    minAvail,
			MaxUnavailable:  maxUnavail,
		})
	}

	return violations
}

// isPodDisruptable checks if a pod is disruptable for PDB calculations.
// A pod is disruptable if it is Running, not being deleted, and Ready.
func isPodDisruptable(pod *corev1.Pod) bool {
	// Pod must be in Running phase
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	// Pod must not have DeletionTimestamp (not being terminated)
	if pod.DeletionTimestamp != nil {
		return false
	}

	// Pod must have Ready condition set to true
	return podHasReadyCondition(pod)
}

/**
* For the given PDB return a map of pods (string key) to node hosting that pod (string name)
* Also populates the podCache with detailed pod information including creation timestamps
* Only includes pods that are disruptable (Running, not terminating, and Ready)
 */
func findPDBPods(kubeClient *kubernetes.Clientset, pdb *policyv1.PodDisruptionBudget, podCache *PodCache) (map[string]string, error) {

	namespace := pdb.GetNamespace()
	selector := pdb.Spec.Selector

	if namespace == "" {
		return nil, fmt.Errorf("can't search for pods without NS")
	}
	if selector == nil {
		return nil, fmt.Errorf("can't search for pods without selector")
	}

	// Convert the LabelSelector into a label.Selector for reading
	readSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("failed to convert label selector %v", err)
	}

	pods, err := kubeClient.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: readSelector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve pods %v", err)
	}

	podData := make(map[string]string)
	for _, pod := range pods.Items {
		// Only include disruptable pods (Running, not terminating, and Ready)
		if !isPodDisruptable(&pod) {
			log.Debugf("Skipping pod %s/%s: phase=%s, deletionTimestamp=%v, ready=%v",
				pod.Namespace, pod.Name, pod.Status.Phase, pod.DeletionTimestamp != nil,
				podHasReadyCondition(&pod))
			continue
		}

		podData[pod.Name] = pod.Spec.NodeName

		// Cache the detailed pod information including creation timestamp
		podInfo := PodInfo{
			Name:              pod.Name,
			Namespace:         pod.Namespace,
			CreationTimestamp: pod.CreationTimestamp.Time,
			NodeName:          pod.Spec.NodeName,
		}
		podCache.AddPod(podInfo)

		//fmt.Printf("Pod %s Node: %s\n", pod.Name, pod.Spec.NodeName)
	}
	return podData, nil
}

// podHasReadyCondition is a helper to check if pod has Ready condition (for logging)
func podHasReadyCondition(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func processPDBs(kubeClient *kubernetes.Clientset, nodeGroups *nodegroup.Collection) ([]PDBViolation, *PodCache, error) {
	var allViolations []PDBViolation
	podCache := NewPodCache()

	pdbList, err := kubeClient.PolicyV1().PodDisruptionBudgets("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return allViolations, podCache, fmt.Errorf("error retrieving PodDisruptionBudgets: %w", err)
	}

	log.Debug("Found PodDisruptionBudgets:")

	// Process PDBs concurrently for better performance
	type pdbResult struct {
		violations []PDBViolation
		err        error
		pdbId      string
	}

	resultChan := make(chan pdbResult, len(pdbList.Items))
	var wg sync.WaitGroup

	// Process each PDB concurrently
	for _, pdb := range pdbList.Items {
		wg.Add(1)
		go func(pdb policyv1.PodDisruptionBudget) {
			defer wg.Done()
			violations, err := processSinglePDB(kubeClient, &pdb, podCache, nodeGroups)
			pdbId := fmt.Sprintf("%s/%s", pdb.GetNamespace(), pdb.GetName())
			resultChan <- pdbResult{violations, err, pdbId}
		}(pdb)
	}

	// Close the channel when all goroutines are done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	for result := range resultChan {
		if result.err != nil {
			log.Warnf("Failed to check PDB %s for violations: %v\n", result.pdbId, result.err)
			continue
		}
		allViolations = append(allViolations, result.violations...)
	}

	return allViolations, podCache, nil
}

// processSinglePDB handles the processing of a single PDB
func processSinglePDB(kubeClient *kubernetes.Clientset, pdb *policyv1.PodDisruptionBudget, podCache *PodCache, nodeGroups *nodegroup.Collection) ([]PDBViolation, error) {
	pdbName := pdb.GetName()
	pdbNamespace := pdb.GetNamespace()

	log.Debugf("Processing PDB: %s/%s", pdbNamespace, pdbName)

	// Find pods for this PDB
	podToNode, err := findPDBPods(kubeClient, pdb, podCache)
	if err != nil {
		return nil, fmt.Errorf("failed to find pods for PDB %s/%s: %w", pdbNamespace, pdbName, err)
	}

	// Check for violations
	return checkPDBPods(pdbName, pdbNamespace, pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, podToNode, nodeGroups)
}

// displayPDBViolationSummary shows a summary of all PDB violations found
func displayPDBViolationSummary(violations []PDBViolation) {
	if len(violations) == 0 {
		log.Infof("No PDB violations detected.")
		return
	}

	fmt.Printf("\n=== PDB Violation Summary ===\n")
	fmt.Printf("Found %d PDB violation(s)\n\n", len(violations))

	if isVerbose {
		for i, violation := range violations {
			fmt.Printf("%d. PDB: %s\n", i+1, violation.ID())
			fmt.Printf("   Violated by NodeGroup: %s\n", violation.NodeGroupName)
			fmt.Printf("   Pods in NodeGroup: %d (max disruptable: %d)\n", violation.PodsInGroup, violation.MaxDisruptable)
			fmt.Printf("   Total PDB pods: %d\n", violation.PodCount)
			fmt.Printf("   Affected pods in this NodeGroup:\n")
			for _, podName := range violation.PodsInNodeGroup {
				fmt.Printf("     - %s\n", podName)
			}
			fmt.Printf("\n")
		}
	}

	// Group violations by NodeGroup for additional insights
	groupViolations := groupViolationsByNodeGroup(violations)
	displayViolationsByNodeGroup(groupViolations)
}

// groupViolationsByNodeGroup organizes violations by node group
func groupViolationsByNodeGroup(violations []PDBViolation) map[string][]PDBViolation {
	groupViolations := make(map[string][]PDBViolation)
	for _, violation := range violations {
		groupViolations[violation.NodeGroupName] = append(groupViolations[violation.NodeGroupName], violation)
	}
	return groupViolations
}

// displayViolationsByNodeGroup shows violations organized by node group
func displayViolationsByNodeGroup(groupViolations map[string][]PDBViolation) {
	fmt.Printf("=== Violations by NodeGroup ===\n")
	for nodeGroup, nodeViolations := range groupViolations {
		fmt.Printf("NodeGroup '%s' violates %d PDB(s)\n", nodeGroup, len(nodeViolations))
		if isVerbose {
			for _, violation := range nodeViolations {
				fmt.Printf("  - %s (%d pods affected)\n", violation.ID(), len(violation.PodsInNodeGroup))
			}
			fmt.Printf("\n")
		}
	}
}

// PodInfo holds detailed information about a pod including creation timestamp
type PodInfo struct {
	Name              string
	Namespace         string
	CreationTimestamp time.Time
	NodeName          string
}

// PodCache stores pod information to avoid redundant API calls
type PodCache struct {
	pods map[string]PodInfo // key is "namespace/podname"
	mu   sync.RWMutex       // protect concurrent access
}

// NewPodCache creates a new pod cache
func NewPodCache() *PodCache {
	return &PodCache{
		pods: make(map[string]PodInfo),
	}
}

// AddPod adds a pod to the cache
func (pc *PodCache) AddPod(podInfo PodInfo) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	key := fmt.Sprintf("%s/%s", podInfo.Namespace, podInfo.Name)
	pc.pods[key] = podInfo
}

// GetPod retrieves a pod from the cache
func (pc *PodCache) GetPod(namespace, name string) (PodInfo, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	key := fmt.Sprintf("%s/%s", namespace, name)
	pod, exists := pc.pods[key]
	return pod, exists
}

// GetPodsInNamespace returns all cached pods in a specific namespace
func (pc *PodCache) GetPodsInNamespace(namespace string) []PodInfo {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	var pods []PodInfo
	prefix := namespace + "/"
	for key, podInfo := range pc.pods {
		if strings.HasPrefix(key, prefix) {
			pods = append(pods, podInfo)
		}
	}
	return pods
}

// promptUserYesNo asks the user a yes/no question and returns true for yes/y
func promptUserYesNo(question string) bool {
	fmt.Printf("%s (y/n): ", question)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		log.Errorf("Error reading user input: %v", err)
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// getDetailedPodInfo retrieves detailed information about pods from cache (avoiding API calls)
// Falls back to batched API calls only if pods are not in cache
func getDetailedPodInfo(kubeClient *kubernetes.Clientset, podNames []string, namespace string, podCache *PodCache) ([]PodInfo, error) {
	var podInfos []PodInfo
	var missingPods []string

	// First pass: collect from cache
	for _, podName := range podNames {
		if cachedPod, exists := podCache.GetPod(namespace, podName); exists {
			podInfos = append(podInfos, cachedPod)
		} else {
			missingPods = append(missingPods, podName)
		}
	}

	// If we have missing pods, fetch them all at once
	if len(missingPods) > 0 {
		log.Debugf("Fetching %d pods not found in cache from namespace %s", len(missingPods), namespace)

		// Use a label selector to fetch all pods in the namespace (more efficient than individual calls)
		pods, err := kubeClient.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Warnf("Could not retrieve pods in namespace %s: %v", namespace, err)
		} else {
			// Create a map of pod names for quick lookup
			missingPodMap := make(map[string]bool)
			for _, podName := range missingPods {
				missingPodMap[podName] = true
			}

			// Find the missing pods in the list
			for _, pod := range pods.Items {
				if missingPodMap[pod.Name] {
					podInfo := PodInfo{
						Name:              pod.Name,
						Namespace:         pod.Namespace,
						CreationTimestamp: pod.CreationTimestamp.Time,
						NodeName:          pod.Spec.NodeName,
					}
					podInfos = append(podInfos, podInfo)

					// Add to cache for future use
					podCache.AddPod(podInfo)
				}
			}
		}
	}

	// Sort by creation timestamp (newest first)
	sort.Slice(podInfos, func(i, j int) bool {
		return podInfos[i].CreationTimestamp.After(podInfos[j].CreationTimestamp)
	})

	return podInfos, nil
}

// checkCurrentPDBStatus checks if deleting a pod would violate the PDB
func checkCurrentPDBStatus(kubeClient *kubernetes.Clientset, pdbNamespace, pdbName string, _ PodInfo) (bool, error) {
	// Get the current PDB
	pdb, err := kubeClient.PolicyV1().PodDisruptionBudgets(pdbNamespace).Get(context.TODO(), pdbName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get PDB %s/%s: %w", pdbNamespace, pdbName, err)
	}

	// Get current pods covered by this PDB (use temporary cache since we want fresh data)
	tempCache := NewPodCache()
	podToNode, err := findPDBPods(kubeClient, pdb, tempCache)
	if err != nil {
		return false, fmt.Errorf("failed to find PDB pods: %w", err)
	}

	currentPodCount := int32(len(podToNode))
	if currentPodCount == 0 {
		return true, nil // No pods left, safe to delete
	}

	// Calculate current requirements using shared logic
	needed, availableForDisruption, err := calculatePDBDisruptionLimits(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, currentPodCount)
	if err != nil {
		return false, err
	}

	log.Debugf("Current PDB status: %d pods, need %d, can disrupt %d", currentPodCount, needed, availableForDisruption)

	// If we can disrupt at least 1 pod, it's safe to delete
	return availableForDisruption > 0, nil
}

// deletePod safely deletes a pod
func deletePod(kubeClient *kubernetes.Clientset, podInfo PodInfo) error {
	err := kubeClient.CoreV1().Pods(podInfo.Namespace).Delete(context.TODO(), podInfo.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete pod %s/%s: %w", podInfo.Namespace, podInfo.Name, err)
	}

	fmt.Printf("Successfully deleted pod %s/%s\n", podInfo.Namespace, podInfo.Name)
	return nil
}

// fixPDBViolationsInteractively processes PDB violations and offers to fix them by deleting pods
func fixPDBViolationsInteractively(kubeClient *kubernetes.Clientset, violations []PDBViolation, podCache *PodCache) error {
	if len(violations) == 0 {
		fmt.Println("No PDB violations to fix.")
		return nil
	}

	fmt.Printf("\n=== Interactive PDB Violation Remediation ===\n")
	fmt.Printf("Found %d PDB violation(s) that can potentially be fixed by deleting pods.\n\n", len(violations))

	for i, violation := range violations {
		fmt.Printf("Violation %d/%d:\n", i+1, len(violations))
		fmt.Printf("  PDB: %s\n", violation.ID())
		fmt.Printf("  NodeGroup: %s\n", violation.NodeGroupName)
		fmt.Printf("  Problem: %d pods in NodeGroup, but max disruptable is %d\n", violation.PodsInGroup, violation.MaxDisruptable)

		// Calculate how many pods need to be deleted
		podsToDelete := violation.PodsInGroup - int(violation.MaxDisruptable)
		if podsToDelete <= 0 {
			fmt.Printf("  No pods need to be deleted (already within limits).\n\n")
			continue
		}

		fmt.Printf("  Solution: Delete %d pod(s) from NodeGroup %s\n", podsToDelete, violation.NodeGroupName)

		// Ask user if they want to fix this violation
		if !promptUserYesNo(fmt.Sprintf("Do you want to fix this violation by deleting %d pod(s)?", podsToDelete)) {
			fmt.Printf("  Skipping violation for %s\n\n", violation.ID())
			continue
		}

		// Get detailed information about the pods
		fmt.Printf("  Getting detailed pod information...\n")
		podInfos, err := getDetailedPodInfo(kubeClient, violation.PodsInNodeGroup, violation.PDBNamespace, podCache)
		if err != nil {
			log.Errorf("Failed to get detailed pod information: %v", err)
			continue
		}

		if len(podInfos) == 0 {
			fmt.Printf("  No pods found to delete. Skipping.\n\n")
			continue
		}

		fmt.Printf("  Pods in NodeGroup %s (sorted by creation time, newest first):\n", violation.NodeGroupName)
		for j, pod := range podInfos {
			fmt.Printf("    %d. %s (created: %s)\n", j+1, pod.Name, pod.CreationTimestamp.Format(time.RFC3339))
		}

		// Delete pods starting with the newest
		deletedCount := 0
		for j := 0; j < len(podInfos) && deletedCount < podsToDelete; j++ {
			podInfo := podInfos[j]

			fmt.Printf("  Checking if pod %s can be safely deleted...\n", podInfo.Name)

			// Check current PDB status
			canDelete, err := checkCurrentPDBStatus(kubeClient, violation.PDBNamespace, violation.PDBName, podInfo)
			if err != nil {
				log.Errorf("Failed to check PDB status: %v", err)
				continue
			}

			if !canDelete {
				fmt.Printf("  WARNING: Deleting pod %s would violate the PDB. Stopping processing of this violation and moving to next PDB violation.\n", podInfo.Name)
				break
			}

			// Delete the pod
			fmt.Printf("  Deleting pod %s...\n", podInfo.Name)
			err = deletePod(kubeClient, podInfo)
			if err != nil {
				log.Errorf("Failed to delete pod %s: %v", podInfo.Name, err)
				continue
			}

			deletedCount++

			// Give some time for the cluster to update
			if deletedCount < podsToDelete {
				fmt.Printf("  Waiting 5 seconds for cluster to update...\n")
				time.Sleep(5 * time.Second)
			}
		}

		if deletedCount == 0 {
			fmt.Printf("  No pods were deleted due to PDB constraints.\n")
		} else if deletedCount < podsToDelete {
			fmt.Printf("  Only deleted %d out of %d required pods due to PDB constraints.\n", deletedCount, podsToDelete)
		} else {
			fmt.Printf("  Successfully deleted %d pod(s). Violation should now be resolved.\n", deletedCount)
		}

		fmt.Printf("\n")
	}

	return nil
}

func main() {
	config := parseFlags()
	setupLogging(config.loglevel, config.debug)

	if config.kubeconfig == "" {
		log.Fatalf("No kubeconfig provided")
	}

	// If the flag was explicitly passed, verify that the file exists
	if config.kubeconfigExplicit {
		if _, err := os.Stat(config.kubeconfig); os.IsNotExist(err) {
			log.Fatalf("kubeconfig file not found: %s", config.kubeconfig)
		}
	}

	kubeClient, tconfig := initializeClients(config.kubeconfig)
	violations, podCache := analyzePDBViolations(kubeClient, tconfig)

	displayPDBViolationSummary(violations)

	if config.fixViolations && len(violations) > 0 {
		if err := fixPDBViolationsInteractively(kubeClient, violations, podCache); err != nil {
			log.Errorf("Error during interactive remediation: %v", err)
		}
	}
}

// Config holds command-line configuration
type Config struct {
	kubeconfig         string
	kubeconfigExplicit bool
	loglevel           string
	debug              bool
	fixViolations      bool
}

// parseFlags parses command line flags and returns configuration
func parseFlags() Config {
	var config Config

	flag.StringVar(&config.kubeconfig, "kubeconfig", "", "Path to kubeconfig. Default is to use env var KUBECONFIG")
	flag.StringVar(&config.loglevel, "loglevel", "", "Log level [debug|info|warn|error]")
	flag.BoolVar(&config.debug, "debug", false, "Set log level to debug (overrides loglevel)")
	flag.BoolVar(&isVerbose, "verbose", false, "Enable verbose output")
	flag.BoolVar(&includeAll, "all", false, "Include violations from serial upgrade MCPs (maxUnavailable=1)")
	flag.BoolVar(&config.fixViolations, "fix-violations", false, "Enable interactive mode to fix PDB violations by deleting pods")

	flag.Parse()

	// Check if the -kubeconfig flag was explicitly passed
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "kubeconfig" {
			config.kubeconfigExplicit = true
		}
	})

	// If the flag was not passed, use the environment variable
	if !config.kubeconfigExplicit {
		config.kubeconfig = os.Getenv("KUBECONFIG")
	}

	return config
}

// setupLogging configures the logging level
func setupLogging(loglevel string, debug bool) {
	if debug {
		log.SetLevel(log.DebugLevel)
		return
	}

	switch loglevel {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
}

// initializeClients creates Kubernetes clients
func initializeClients(kubeconfig string) (*kubernetes.Clientset, *rest.Config) {
	var tconfig *rest.Config
	var err error

	// If an explicit path was provided, use it directly
	if kubeconfig != "" {
		tconfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Fatalf("Failed to build config from kubeconfig file %s: %v", kubeconfig, err)
		}
	} else {
		// If not provided, use default rules (which include the environment variable)
		tconfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			log.Fatalf("Failed to initialize config client: %v", err)
		}
	}

	kubeClient := kubernetes.NewForConfigOrDie(tconfig)
	return kubeClient, tconfig
}

// analyzePDBViolations performs the main analysis
func analyzePDBViolations(kubeClient *kubernetes.Clientset, tconfig *rest.Config) ([]PDBViolation, *PodCache) {
	// Get node groups
	nodeGroups, err := groupNodesByMCP(tconfig, kubeClient)
	if err != nil {
		log.Fatalf("Failed to get node groupings: %v", err)
	}

	// Analyze PDBs
	violations, podCache, err := processPDBs(kubeClient, nodeGroups)
	if err != nil {
		log.Fatalf("Failed to process PDBs: %v", err)
	}

	return violations, podCache
}
