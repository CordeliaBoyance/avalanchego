// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package warpbench

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ava-labs/avalanchego/chains"
	"github.com/ava-labs/avalanchego/chains/atomic"
	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/codec/linearcodec"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/database/manager"
	"github.com/ava-labs/avalanchego/database/prefixdb"
	"github.com/ava-labs/avalanchego/database/versiondb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/snow/engine/common"
	"github.com/ava-labs/avalanchego/snow/uptime"
	"github.com/ava-labs/avalanchego/snow/validators"
	"github.com/ava-labs/avalanchego/utils"
	"github.com/ava-labs/avalanchego/utils/constants"
	"github.com/ava-labs/avalanchego/utils/crypto/secp256k1"
	"github.com/ava-labs/avalanchego/utils/formatting"
	"github.com/ava-labs/avalanchego/utils/formatting/address"
	"github.com/ava-labs/avalanchego/utils/json"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/timer/mockable"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/utils/wrappers"
	"github.com/ava-labs/avalanchego/version"
	"github.com/ava-labs/avalanchego/vms/components/avax"
	"github.com/ava-labs/avalanchego/vms/platformvm/api"
	"github.com/ava-labs/avalanchego/vms/platformvm/config"
	"github.com/ava-labs/avalanchego/vms/platformvm/fx"
	"github.com/ava-labs/avalanchego/vms/platformvm/metrics"
	"github.com/ava-labs/avalanchego/vms/platformvm/reward"
	"github.com/ava-labs/avalanchego/vms/platformvm/state"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/executor"
	"github.com/ava-labs/avalanchego/vms/platformvm/txs/mempool"
	"github.com/ava-labs/avalanchego/vms/platformvm/utxo"
	"github.com/ava-labs/avalanchego/vms/secp256k1fx"

	blockexecutor "github.com/ava-labs/avalanchego/vms/platformvm/blocks/executor"
	p_tx_builder "github.com/ava-labs/avalanchego/vms/platformvm/txs/builder"
	pvalidators "github.com/ava-labs/avalanchego/vms/platformvm/validators"
)

const (
	testNetworkID = 10 // To be used in tests
	defaultWeight = 10000
)

var (
	_ mempool.BlockTimer = (*environment)(nil)

	defaultMinStakingDuration = 24 * time.Hour
	defaultMaxStakingDuration = 365 * 24 * time.Hour
	defaultGenesisTime        = time.Date(1997, 1, 1, 0, 0, 0, 0, time.UTC)
	defaultValidateStartTime  = defaultGenesisTime
	defaultValidateEndTime    = defaultValidateStartTime.Add(10 * defaultMinStakingDuration)
	defaultMinValidatorStake  = 5 * units.MilliAvax
	defaultBalance            = 100 * defaultMinValidatorStake
	preFundedKeys             = secp256k1.TestKeys()
	avaxAssetID               = ids.ID{'y', 'e', 'e', 't'}
	defaultTxFee              = uint64(100)
	xChainID                  = ids.Empty.Prefix(0)
	cChainID                  = ids.Empty.Prefix(1)

	genesisBlkID ids.ID

	errMissingPrimaryValidators = errors.New("missing primary validator set")
	errMissing                  = errors.New("missing")
)

type environment struct {
	blkManager blockexecutor.Manager
	mempool    mempool.Mempool
	sender     *common.SenderTest

	isBootstrapped    *utils.Atomic[bool]
	config            *config.Config
	clk               *mockable.Clock
	baseDB            *versiondb.Database
	ctx               *snow.Context
	fx                fx.Fx
	state             state.State
	atomicUTXOs       avax.AtomicUTXOManager
	uptimes           uptime.Manager
	utxosHandler      utxo.Handler
	txBuilder         p_tx_builder.Builder
	backend           *executor.Backend
	validatorsManager pvalidators.Manager
}

func (*environment) ResetBlockTimer() {
	// dummy call, do nothing for now
}

func newEnvironment(tb testing.TB, subnetIDToRegister ids.ID) *environment {
	env := &environment{
		isBootstrapped: &utils.Atomic[bool]{},
		config:         defaultConfig(),
		clk:            defaultClock(),
	}
	env.config.TrackedSubnets.Add(subnetIDToRegister)
	env.isBootstrapped.Set(true)

	dir := tb.TempDir()
	baseDBManager, err := manager.NewLevelDB(
		dir,
		nil,
		logging.NoLog{},
		version.Semantic1_0_0,
		"",
		prometheus.NewRegistry(),
	)
	if err != nil {
		panic(fmt.Errorf("failed to create database: %w", err))
	}

	env.baseDB = versiondb.New(baseDBManager.Current().Database)
	env.ctx = defaultCtx(env.baseDB)
	env.fx = defaultFx(env.clk, env.ctx.Log, env.isBootstrapped.Get())

	rewardsCalc := reward.NewCalculator(env.config.RewardConfig)
	env.atomicUTXOs = avax.NewAtomicUTXOManager(env.ctx.SharedMemory, txs.Codec)

	env.state = defaultState(env.config, env.ctx, env.baseDB, rewardsCalc)
	env.uptimes = uptime.NewManager(env.state)
	env.utxosHandler = utxo.NewHandler(env.ctx, env.clk, env.fx)
	env.txBuilder = p_tx_builder.New(
		env.ctx,
		env.config,
		env.clk,
		env.fx,
		env.state,
		env.atomicUTXOs,
		env.utxosHandler,
	)

	env.backend = &executor.Backend{
		Config:       env.config,
		Ctx:          env.ctx,
		Clk:          env.clk,
		Bootstrapped: env.isBootstrapped,
		Fx:           env.fx,
		FlowChecker:  env.utxosHandler,
		Uptimes:      env.uptimes,
		Rewards:      rewardsCalc,
	}

	registerer := prometheus.NewRegistry()
	env.sender = &common.SenderTest{}

	metrics := metrics.Noop

	env.mempool, err = mempool.NewMempool("mempool", registerer, env)
	if err != nil {
		panic(fmt.Errorf("failed to create mempool: %w", err))
	}

	env.validatorsManager = pvalidators.NewManager(
		*env.config,
		env.state,
		metrics,
		env.clk,
	)

	env.blkManager = blockexecutor.NewManager(
		env.mempool,
		metrics,
		env.state,
		env.backend,
		env.validatorsManager,
	)

	return env
}

func defaultState(
	cfg *config.Config,
	ctx *snow.Context,
	db database.Database,
	rewards reward.Calculator,
) state.State {
	genesisBytes := buildGenesisTest(ctx)
	state, err := state.New(
		db,
		genesisBytes,
		prometheus.NewRegistry(),
		cfg,
		ctx,
		metrics.Noop,
		rewards,
		&utils.Atomic[bool]{},
	)
	if err != nil {
		panic(err)
	}

	// persist and reload to init a bunch of in-memory stuff
	state.SetHeight(0)
	if err := state.Commit(); err != nil {
		panic(err)
	}
	genesisBlkID = state.GetLastAccepted()
	return state
}

func defaultCtx(db database.Database) *snow.Context {
	ctx := snow.DefaultContextTest()
	ctx.NetworkID = 10
	ctx.XChainID = xChainID
	ctx.CChainID = cChainID
	ctx.AVAXAssetID = avaxAssetID

	atomicDB := prefixdb.New([]byte{1}, db)
	m := atomic.NewMemory(atomicDB)

	ctx.SharedMemory = m.NewSharedMemory(ctx.ChainID)

	ctx.ValidatorState = &validators.TestState{
		GetSubnetIDF: func(_ context.Context, chainID ids.ID) (ids.ID, error) {
			subnetID, ok := map[ids.ID]ids.ID{
				constants.PlatformChainID: constants.PrimaryNetworkID,
				xChainID:                  constants.PrimaryNetworkID,
				cChainID:                  constants.PrimaryNetworkID,
			}[chainID]
			if !ok {
				return ids.Empty, errMissing
			}
			return subnetID, nil
		},
	}

	return ctx
}

func defaultConfig() *config.Config {
	vdrs := validators.NewManager()
	primaryVdrs := validators.NewSet()
	_ = vdrs.Add(constants.PrimaryNetworkID, primaryVdrs)
	return &config.Config{
		Chains:                 chains.TestManager,
		UptimeLockedCalculator: uptime.NewLockedCalculator(),
		Validators:             vdrs,
		TxFee:                  defaultTxFee,
		CreateSubnetTxFee:      100 * defaultTxFee,
		CreateBlockchainTxFee:  100 * defaultTxFee,
		MinValidatorStake:      5 * units.MilliAvax,
		MaxValidatorStake:      500 * units.MilliAvax,
		MinDelegatorStake:      1 * units.MilliAvax,
		MinStakeDuration:       defaultMinStakingDuration,
		MaxStakeDuration:       defaultMaxStakingDuration,
		RewardConfig: reward.Config{
			MaxConsumptionRate: .12 * reward.PercentDenominator,
			MinConsumptionRate: .10 * reward.PercentDenominator,
			MintingPeriod:      365 * 24 * time.Hour,
			SupplyCap:          720 * units.MegaAvax,
		},
		ApricotPhase3Time: defaultValidateEndTime,
		ApricotPhase5Time: defaultValidateEndTime,
		BanffTime:         mockable.MaxTime,
	}
}

func defaultClock() *mockable.Clock {
	clk := &mockable.Clock{}
	clk.Set(defaultGenesisTime)
	return clk
}

type fxVMInt struct {
	registry codec.Registry
	clk      *mockable.Clock
	log      logging.Logger
}

func (fvi *fxVMInt) CodecRegistry() codec.Registry {
	return fvi.registry
}

func (fvi *fxVMInt) Clock() *mockable.Clock {
	return fvi.clk
}

func (fvi *fxVMInt) Logger() logging.Logger {
	return fvi.log
}

func defaultFx(clk *mockable.Clock, log logging.Logger, isBootstrapped bool) fx.Fx {
	fxVMInt := &fxVMInt{
		registry: linearcodec.NewDefault(),
		clk:      clk,
		log:      log,
	}
	res := &secp256k1fx.Fx{}
	if err := res.Initialize(fxVMInt); err != nil {
		panic(err)
	}
	if isBootstrapped {
		if err := res.Bootstrapped(); err != nil {
			panic(err)
		}
	}
	return res
}

// buildGenesisTest creates the genesis bytes required to initialize state.State
// (in prod code they are passed via vm.Initialize). The test genesis contains three
// test validators whose control keys are available for tests
func buildGenesisTest(ctx *snow.Context) []byte {
	genesisUTXOs := make([]api.UTXO, len(preFundedKeys))
	hrp := constants.NetworkIDToHRP[testNetworkID]
	for i, key := range preFundedKeys {
		id := key.PublicKey().Address()
		addr, err := address.FormatBech32(hrp, id.Bytes())
		if err != nil {
			panic(err)
		}
		genesisUTXOs[i] = api.UTXO{
			Amount:  json.Uint64(defaultBalance),
			Address: addr,
		}
	}

	genesisValidators := make([]api.PermissionlessValidator, len(preFundedKeys))
	for i, key := range preFundedKeys {
		nodeID := ids.NodeID(key.PublicKey().Address())
		addr, err := address.FormatBech32(hrp, nodeID.Bytes())
		if err != nil {
			panic(err)
		}
		genesisValidators[i] = api.PermissionlessValidator{
			Staker: api.Staker{
				StartTime: json.Uint64(defaultValidateStartTime.Unix()),
				EndTime:   json.Uint64(defaultValidateEndTime.Unix()),
				NodeID:    nodeID,
			},
			RewardOwner: &api.Owner{
				Threshold: 1,
				Addresses: []string{addr},
			},
			Staked: []api.UTXO{{
				Amount:  json.Uint64(defaultWeight),
				Address: addr,
			}},
			DelegationFee: reward.PercentDenominator,
		}
	}

	buildGenesisArgs := api.BuildGenesisArgs{
		NetworkID:     json.Uint32(testNetworkID),
		AvaxAssetID:   ctx.AVAXAssetID,
		UTXOs:         genesisUTXOs,
		Validators:    genesisValidators,
		Chains:        nil,
		Time:          json.Uint64(defaultGenesisTime.Unix()),
		InitialSupply: json.Uint64(360 * units.MegaAvax),
		Encoding:      formatting.Hex,
	}

	buildGenesisResponse := api.BuildGenesisReply{}
	platformvmSS := api.StaticService{}
	if err := platformvmSS.BuildGenesis(nil, &buildGenesisArgs, &buildGenesisResponse); err != nil {
		panic(fmt.Errorf("problem while building platform chain's genesis state: %w", err))
	}

	genesisBytes, err := formatting.Decode(buildGenesisResponse.Encoding, buildGenesisResponse.Bytes)
	if err != nil {
		panic(err)
	}

	return genesisBytes
}

func shutdownEnvironment(t *environment) error {
	if t.isBootstrapped.Get() {
		primaryValidatorSet, exist := t.config.Validators.Get(constants.PrimaryNetworkID)
		if !exist {
			return errMissingPrimaryValidators
		}
		primaryValidators := primaryValidatorSet.List()

		validatorIDs := make([]ids.NodeID, len(primaryValidators))
		for i, vdr := range primaryValidators {
			validatorIDs[i] = vdr.NodeID
		}

		if err := t.uptimes.StopTracking(validatorIDs, constants.PrimaryNetworkID); err != nil {
			return err
		}
		if err := t.state.Commit(); err != nil {
			return err
		}
	}

	errs := wrappers.Errs{}
	if t.state != nil {
		errs.Add(t.state.Close())
	}
	errs.Add(t.baseDB.Close())
	return errs.Err
}