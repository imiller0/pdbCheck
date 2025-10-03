/****************************************************************
 *
 * This code was developed with AI code generation assistance
 *
 ****************************************************************/

package nodegroup

import (
	"fmt"

	machineconfigv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// NodeGroup represents a group of nodes that are upgraded together
type NodeGroup struct {
	Name string
	// Set of nodes in this group. bool is true for all entries
	Nodes map[string]bool
	// True if this node group is limited to serial upgrades
	SerialUpgrade bool
}

// Collection manages a collection of NodeGroups
type Collection struct {
	Groups      map[string]*NodeGroup
	NodeToGroup map[string]string
}

// New creates a new NodeGroup with initialized fields
func New() *NodeGroup {
	return &NodeGroup{
		Name:          "",
		Nodes:         make(map[string]bool),
		SerialUpgrade: false,
	}
}

// NewCollection creates a new NodeGroup collection
func NewCollection() *Collection {
	return &Collection{
		Groups:      make(map[string]*NodeGroup),
		NodeToGroup: make(map[string]string),
	}
}

// NodeCount returns the number of nodes in this group
func (ng *NodeGroup) NodeCount() int {
	return len(ng.Nodes)
}

// AddNode adds a node to this group
func (ng *NodeGroup) AddNode(nodeName string) {
	ng.Nodes[nodeName] = true
}

// HasNode checks if a node is in this group
func (ng *NodeGroup) HasNode(nodeName string) bool {
	return ng.Nodes[nodeName]
}

// ShouldIncludeInAnalysis determines if this nodegroup should be included in PDB violation analysis
// based on the serial upgrade setting and global flags
func (ng *NodeGroup) ShouldIncludeInAnalysis(includeAll bool) bool {
	// Include if not serial upgrade, or if -all flag is set
	return !ng.SerialUpgrade || includeAll
}

// CreateFromMCP creates a NodeGroup from a Machine Config Pool
func CreateFromMCP(mcp machineconfigv1.MachineConfigPool, nodeMap map[string]*corev1.Node) (*NodeGroup, error) {
	if shouldSkipMCP(mcp) {
		return nil, fmt.Errorf("MCP %s should be skipped", mcp.Name)
	}

	labelSelector, err := metav1.LabelSelectorAsSelector(mcp.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to convert nodeSelector: %w", err)
	}

	nodeGroup := New()
	nodeGroup.Name = mcp.Name
	nodeGroup.SerialUpgrade = isSerialUpgrade(mcp)

	// Find matching nodes
	for nodeName, node := range nodeMap {
		if labelSelector.Matches(labels.Set(node.Labels)) {
			nodeGroup.AddNode(nodeName)
		}
	}

	log.Debugf("Created NodeGroup %s: %d nodes, serial upgrade: %v",
		mcp.Name, nodeGroup.NodeCount(), nodeGroup.SerialUpgrade)

	return nodeGroup, nil
}

// AddGroup adds a NodeGroup to the collection
func (c *Collection) AddGroup(nodeGroup *NodeGroup) {
	c.Groups[nodeGroup.Name] = nodeGroup

	// Update node-to-group mapping
	for nodeName := range nodeGroup.Nodes {
		c.NodeToGroup[nodeName] = nodeGroup.Name
	}
}

// GetGroup retrieves a NodeGroup by name
func (c *Collection) GetGroup(name string) (*NodeGroup, bool) {
	group, exists := c.Groups[name]
	return group, exists
}

// GetGroupForNode returns the NodeGroup that contains the specified node
func (c *Collection) GetGroupForNode(nodeName string) (*NodeGroup, bool) {
	groupName, exists := c.NodeToGroup[nodeName]
	if !exists {
		return nil, false
	}
	return c.GetGroup(groupName)
}

// GroupCount returns the number of groups in the collection
func (c *Collection) GroupCount() int {
	return len(c.Groups)
}

// shouldSkipMCP determines if an MCP should be skipped
func shouldSkipMCP(mcp machineconfigv1.MachineConfigPool) bool {
	if mcp.Status.MachineCount == 0 {
		log.Debugf("Skipping empty MCP %s", mcp.Name)
		return true
	}
	if mcp.Spec.NodeSelector == nil {
		log.Infof("Skipping MCP %s with no node selector", mcp.Name)
		return true
	}
	return false
}

// isSerialUpgrade determines if an MCP uses serial upgrades
func isSerialUpgrade(mcp machineconfigv1.MachineConfigPool) bool {
	return mcp.Spec.MaxUnavailable == nil || mcp.Spec.MaxUnavailable.IntVal == 1
}

// BuildFromMCPs creates a Collection of NodeGroups from Machine Config Pools
func BuildFromMCPs(mcpList *machineconfigv1.MachineConfigPoolList, nodeMap map[string]*corev1.Node) (*Collection, error) {
	collection := NewCollection()

	for _, mcp := range mcpList.Items {
		nodeGroup, err := CreateFromMCP(mcp, nodeMap)
		if err != nil {
			// Skip MCPs that should be skipped, but return errors for real failures
			if shouldSkipMCP(mcp) {
				continue
			}
			return nil, fmt.Errorf("failed to create node group for MCP %s: %w", mcp.Name, err)
		}

		collection.AddGroup(nodeGroup)
		log.Debugf("Added NodeGroup %s with %d nodes (serial: %v)",
			nodeGroup.Name, nodeGroup.NodeCount(), nodeGroup.SerialUpgrade)
	}

	return collection, nil
}
