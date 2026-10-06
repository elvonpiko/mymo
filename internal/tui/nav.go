package tui

// screenKind identifies one of the workspace's focused views.
type screenKind int

const (
	scHome            screenKind = iota // the home hub: what mymo is, where to go
	scFleet                             // the node list
	scNode                              // one node: facts and actions
	scNodeApps                          // applications on a node
	scNodeInspect                       // a node's full stored record
	scNodePreflight                     // a node's app-host preflight verdicts
	scNodePlan                          // a node's generated baseline plan
	scNodeApplyReport                   // the apply's report and verification rows
	scAppDetail                         // one application: its releases and exposure
	scApps                              // applications across the fleet
	scDeploy                            // deploy workflow entry point
	scSettings                          // settings and about
	scAddNode                           // add-node workflow (overlay)
)

// screen is one entry of the navigation stack.
type screen struct {
	kind screenKind
	node string // node context for node-related screens
	app  string // app context for app-related screens
}

// push appends a screen to the navigation stack and re-lays out the
// canvas for the page that just gained focus.
func (m *Model) push(s screen) {
	m.stack = append(m.stack, s)
	m.layout()
}

// pop removes the top screen of the navigation stack, keeping the root.
// Popping the add-node workflow also resets its state. The canvas
// re-lays out for the page that resurfaces.
func (m *Model) pop() {
	if len(m.stack) <= 1 {
		return
	}
	if m.stack[len(m.stack)-1].kind == scAddNode {
		m.addNode = addNodeState{}
	}
	m.stack = m.stack[:len(m.stack)-1]
	m.layout()
}

// cur returns the top of the navigation stack.
func (m *Model) cur() screen { return m.stack[len(m.stack)-1] }

// crumbs returns the breadcrumb parts for the header, derived from the
// navigation stack: "mymo / prod-01 / applications".
func (m *Model) crumbs() []string {
	var parts []string
	for _, s := range m.stack {
		switch s.kind {
		case scFleet:
			parts = append(parts, "fleet")
		case scNode:
			parts = append(parts, s.node)
		case scNodeApps:
			parts = append(parts, "applications")
		case scAppDetail:
			parts = append(parts, s.app)
		case scNodeInspect:
			parts = append(parts, "inspect")
		case scNodePreflight:
			parts = append(parts, "check")
		case scNodePlan:
			parts = append(parts, "plan")
		case scNodeApplyReport:
			parts = append(parts, "apply")
		case scApps:
			parts = append(parts, "applications")
		case scDeploy:
			parts = append(parts, "deploy")
		case scSettings:
			parts = append(parts, "settings")
		case scAddNode:
			parts = append(parts, "add node")
		}
	}
	return parts
}
