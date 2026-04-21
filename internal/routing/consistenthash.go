package routing

import (
	"hash/fnv"
	"sort"
	"strconv"
)

// Ring is a minimal consistent-hash ring suitable for shard-to-pod mapping.
// It is intentionally simple (no virtual nodes) since v1 focuses on correctness
// and stable routing semantics rather than perfect balance.
type Ring struct {
	nodes []string
}

func NewRing(nodes []string) *Ring {
	cp := append([]string(nil), nodes...)
	sort.Strings(cp)
	return &Ring{nodes: cp}
}

func (r *Ring) Empty() bool {
	return len(r.nodes) == 0
}

// Pick chooses a node for the given shard ID.
func (r *Ring) Pick(shardID int32) string {
	if len(r.nodes) == 0 {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.Itoa(int(shardID))))
	idx := int(h.Sum32()) % len(r.nodes)
	return r.nodes[idx]
}
