package routing

import (
	"encoding/json"
	"errors"
)

type ShardMap struct {
	Version int
	Shards  []ShardAssignment
}

type ShardAssignment struct {
	ShardID  int32
	Role     string
	OwnerPod string
}

func ParseShardMapJSON(b []byte) (*ShardMap, error) {
	if len(b) == 0 {
		return nil, errors.New("empty shard map")
	}
	var raw struct {
		Version int `json:"version"`
		Shards  []struct {
			ShardID  int32  `json:"shardID"`
			Role     string `json:"role"`
			OwnerPod string `json:"ownerPod,omitempty"`
		} `json:"shards"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	m := &ShardMap{Version: raw.Version, Shards: make([]ShardAssignment, 0, len(raw.Shards))}
	for _, s := range raw.Shards {
		m.Shards = append(m.Shards, ShardAssignment{ShardID: s.ShardID, Role: s.Role, OwnerPod: s.OwnerPod})
	}
	return m, nil
}
