package check

import (
	"context"
	"sort"
)

// matcher finds every pattern of a set in a text in one pass over it, the
// automaton of Aho and Corasick (Communications of the ACM 18(6), 1975): a
// trie of the patterns whose every node knows the longest proper suffix of
// its path that is also a path (fail) and the nearest such suffix that
// ends a pattern (dict). The work of a pass is the bytes of the text, plus
// the steps back along fail links, at most as many again, plus one per
// pattern ending at each position: linear in the text whatever the
// patterns, but for that last term, which a budget bounds.
type matcher struct {
	nodes []acNode
	// root is the full transition table of the root, the node most
	// visited.
	root [256]int32
	// lens is each pattern's length in bytes.
	lens []int
}

// acNode is a node of the trie.
type acNode struct {
	// edges are the node's children, by byte, sorted.
	edges []acEdge
	fail  int32
	// dict is the nearest node along fail links that ends a pattern, or
	// -1.
	dict int32
	// pat is the pattern that ends here, or -1.
	pat int32
}

// acEdge is a child of a node.
type acEdge struct {
	b    byte
	next int32
}

// child returns the child of n by b, or -1.
func (n *acNode) child(b byte) int32 {
	i := sort.Search(len(n.edges), func(i int) bool { return n.edges[i].b >= b })
	if i < len(n.edges) && n.edges[i].b == b {
		return n.edges[i].next
	}
	return -1
}

// newMatcher builds the automaton of patterns; pattern i is reported as i.
// Equal patterns are the caller's to merge; an empty one is never reported.
func newMatcher(patterns [][]byte) *matcher {
	m := &matcher{nodes: []acNode{{fail: 0, dict: -1, pat: -1}}, lens: make([]int, len(patterns))}
	for i, p := range patterns {
		m.lens[i] = len(p)
		if len(p) == 0 {
			continue
		}
		cur := int32(0)
		for _, b := range p {
			next := m.nodes[cur].child(b)
			if next < 0 {
				next = int32(len(m.nodes)) //nolint:gosec // G115: the nodes are at most the bytes of the patterns, bounded far below 2^31.
				m.nodes = append(m.nodes, acNode{dict: -1, pat: -1})
				n := &m.nodes[cur]
				at := sort.Search(len(n.edges), func(i int) bool { return n.edges[i].b >= b })
				n.edges = append(n.edges, acEdge{})
				copy(n.edges[at+1:], n.edges[at:])
				n.edges[at] = acEdge{b: b, next: next}
			}
			cur = next
		}
		m.nodes[cur].pat = int32(i) //nolint:gosec // G115: the patterns are bounded by the needles' bound.
	}
	// Breadth first, so a node's fail target is finished before it.
	queue := make([]int32, 0, len(m.nodes))
	for _, e := range m.nodes[0].edges {
		m.root[e.b] = e.next
		m.nodes[e.next].fail = 0
		queue = append(queue, e.next)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range m.nodes[cur].edges {
			f := m.nodes[cur].fail
			for {
				if f == 0 {
					m.nodes[e.next].fail = m.root[e.b]
					break
				}
				if c := m.nodes[f].child(e.b); c >= 0 {
					m.nodes[e.next].fail = c
					break
				}
				f = m.nodes[f].fail
			}
			if fn := m.nodes[e.next].fail; m.nodes[fn].pat >= 0 {
				m.nodes[e.next].dict = fn
			} else {
				m.nodes[e.next].dict = m.nodes[fn].dict
			}
			queue = append(queue, e.next)
		}
	}
	return m
}

// errBudget is a pass that would pass its budget of work.
type errBudget struct{}

func (errBudget) Error() string { return "deid check: the search passes its bound" }

// ctxEvery is how many bytes a pass reads between looks at its context.
const ctxEvery = 64 << 10

// scan passes over text once and calls found with each pattern ending at
// each position: the pattern and the offset one past its end. A found
// that returns false ends the pass. Every step costs one unit of *budget;
// past it the pass ends with errBudget. ctx is read every ctxEvery bytes.
func (m *matcher) scan(ctx context.Context, text []byte, budget *int64, found func(pat, end int) bool) error {
	cur := int32(0)
	for i, b := range text {
		if i%ctxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if *budget < 0 {
				return errBudget{}
			}
		}
		for {
			if cur == 0 {
				cur = m.root[b]
				break
			}
			if c := m.nodes[cur].child(b); c >= 0 {
				cur = c
				break
			}
			cur = m.nodes[cur].fail
			*budget--
		}
		*budget--
		n := cur
		if m.nodes[n].pat < 0 {
			n = m.nodes[n].dict
		}
		for n > 0 {
			*budget--
			if !found(int(m.nodes[n].pat), i+1) {
				return nil
			}
			n = m.nodes[n].dict
		}
		if *budget < 0 {
			return errBudget{}
		}
	}
	return nil
}
