// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package snowball

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/bag"
)

var _ Consensus = (*Flat)(nil)

func NewFlat(params Parameters, choice ids.ID) Consensus {
	f := &Flat{
		params: params,
	}
	f.nnarySnowball.Initialize(params.BetaVirtuous, params.BetaRogue, choice)
	return f
}

// Flat is a naive implementation of a multi-choice snowball instance
type Flat struct {
	// wraps the n-nary snowball logic
	nnarySnowball

	// params contains all the configurations of a snowball instance
	params Parameters
}

func (f *Flat) RecordPoll(votes bag.Bag[ids.ID]) bool {
	if pollMode, numVotes := votes.Mode(); numVotes >= f.params.Alpha {
		f.RecordSuccessfulPoll(pollMode)
		return true
	}

	f.RecordUnsuccessfulPoll()
	return false
}
