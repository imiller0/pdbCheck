package nodegroup

// NodeGroup represents a group of nodes that are upgraded together
type NodeGroup struct {
	Name string
	// Set of nodes in this group. bool is true for all entries
	Nodes map[string]bool
	// True if this node group is limited to serial upgrades
	SerialUpgrade bool
}

// New creates a new NodeGroup with initialized fields
func New() *NodeGroup {
	ng := NodeGroup{
		Name:          "",
		Nodes:         make(map[string]bool),
		SerialUpgrade: false,
	}
	return &ng
}
