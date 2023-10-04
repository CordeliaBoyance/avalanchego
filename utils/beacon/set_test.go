// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package beacon

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/ips"
)

func TestSet(t *testing.T) {
	require := require.New(t)

	id0 := ids.TestNodeIDFromBytes([]byte{0}, ids.ShortNodeIDLen)
	id1 := ids.TestNodeIDFromBytes([]byte{1}, ids.ShortNodeIDLen)
	id2 := ids.TestNodeIDFromBytes([]byte{2}, ids.ShortNodeIDLen)

	ip0 := ips.IPPort{
		IP:   net.IPv4zero,
		Port: 0,
	}
	ip1 := ips.IPPort{
		IP:   net.IPv4zero,
		Port: 1,
	}
	ip2 := ips.IPPort{
		IP:   net.IPv4zero,
		Port: 2,
	}

	b0 := New(id0, ip0)
	b1 := New(id1, ip1)
	b2 := New(id2, ip2)

	s := NewSet()

	require.Equal("", s.IDsArg())
	require.Equal("", s.IPsArg())
	require.Zero(s.Len())

	require.NoError(s.Add(b0))

	require.Equal("NodeID-111111111111111111116DBWJs", s.IDsArg())
	require.Equal("0.0.0.0:0", s.IPsArg())
	require.Equal(1, s.Len())

	err := s.Add(b0)
	require.ErrorIs(err, errDuplicateID)

	require.Equal("NodeID-111111111111111111116DBWJs", s.IDsArg())
	require.Equal("0.0.0.0:0", s.IPsArg())
	require.Equal(1, s.Len())

	require.NoError(s.Add(b1))

	require.Equal("NodeID-111111111111111111116DBWJs,NodeID-6HgC8KRBEhXYbF4riJyJFLSHt37UNuRt", s.IDsArg())
	require.Equal("0.0.0.0:0,0.0.0.0:1", s.IPsArg())
	require.Equal(2, s.Len())

	require.NoError(s.Add(b2))

	require.Equal("NodeID-111111111111111111116DBWJs,NodeID-6HgC8KRBEhXYbF4riJyJFLSHt37UNuRt,NodeID-BaMPFdqMUQ46BV8iRcwbVfsam55kMqcp", s.IDsArg())
	require.Equal("0.0.0.0:0,0.0.0.0:1,0.0.0.0:2", s.IPsArg())
	require.Equal(3, s.Len())

	require.NoError(s.RemoveByID(b0.ID()))

	require.Equal("NodeID-BaMPFdqMUQ46BV8iRcwbVfsam55kMqcp,NodeID-6HgC8KRBEhXYbF4riJyJFLSHt37UNuRt", s.IDsArg())
	require.Equal("0.0.0.0:2,0.0.0.0:1", s.IPsArg())
	require.Equal(2, s.Len())

	require.NoError(s.RemoveByIP(b1.IP()))

	require.Equal("NodeID-BaMPFdqMUQ46BV8iRcwbVfsam55kMqcp", s.IDsArg())
	require.Equal("0.0.0.0:2", s.IPsArg())
	require.Equal(1, s.Len())
}
