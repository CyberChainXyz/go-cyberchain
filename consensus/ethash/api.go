// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package ethash

import (
	"context"
	"errors"
	"math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethereum/go-ethereum/rpc"
)

var errEthashStopped = errors.New("ethash stopped")

// API exposes ethash related methods for the RPC interface.
type API struct {
	ethash *Ethash
}

// GetWork returns a work package for external miner.
//
// The work package consists of 3 strings:
//
//	result[0] - 32 bytes hex encoded current block header pow-hash
//	result[1] - 32 bytes hex encoded seed hash used for DAG
//	result[2] - 32 bytes hex encoded boundary condition ("target"), 2^256/difficulty
//	result[3] - hex encoded block number
func (api *API) GetWork() ([4]string, error) {
	if api.ethash.remote == nil {
		return [4]string{}, errors.New("not supported")
	}

	var (
		workCh = make(chan [4]string, 1)
		errc   = make(chan error, 1)
	)
	select {
	case api.ethash.remote.fetchWorkCh <- &sealWork{errc: errc, res: workCh}:
	case <-api.ethash.remote.exitCh:
		return [4]string{}, errEthashStopped
	}
	select {
	case work := <-workCh:
		return work, nil
	case err := <-errc:
		return [4]string{}, err
	}
}

// SubmitWork can be used by external miner to submit their POW solution.
// It returns an indication if the work was accepted.
// Note either an invalid solution, a stale work a non-existent work will return false.
func (api *API) SubmitWorkOld(nonce types.BlockNonce, hash, digest common.Hash) bool {
	if api.ethash.remote == nil {
		return false
	}

	var errc = make(chan error, 1)
	select {
	case api.ethash.remote.submitWorkCh <- &mineResult{
		nonce:     nonce,
		mixDigest: digest,
		hash:      hash,
		errc:      errc,
	}:
	case <-api.ethash.remote.exitCh:
		return false
	}
	err := <-errc
	return err == nil
}

// SubmitWork can be used by external miner to submit their POW solution.
// It returns an indication if the work was accepted.
// Note either an invalid solution, a stale work a non-existent work will return false.
func (api *API) SubmitWork(jobId string, nonce types.BlockNonce, hash common.Hash) bool {
	if api.ethash.remote == nil {
		return false
	}

	var errc = make(chan error, 1)
	select {
	case api.ethash.remote.submitWorkCh <- &mineResult{
		nonce:     nonce,
		mixDigest: common.Hash{},
		hash:      hash,
		errc:      errc,
	}:
	case <-api.ethash.remote.exitCh:
		return false
	}
	err := <-errc
	return err == nil
}

// SubmitHashrate can be used for remote miners to submit their hash rate.
// This enables the node to report the combined hash rate of all miners
// which submit work through this node.
//
// It accepts the miner hash rate and an identifier which must be unique
// between nodes.
func (api *API) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	if api.ethash.remote == nil {
		return false
	}

	var done = make(chan struct{}, 1)
	select {
	case api.ethash.remote.submitRateCh <- &hashrate{done: done, rate: uint64(rate), id: id}:
	case <-api.ethash.remote.exitCh:
		return false
	}

	// Block until hash rate submitted successfully.
	<-done
	return true
}

// GetHashrate returns the current hashrate for local CPU miner and remote miner.
func (api *API) GetHashrate() uint64 {
	return uint64(api.ethash.Hashrate())
}

// newWork notification include jobId for compatibility with mining pool.
func makeJobId(length int) string {
	// Define the characters from which you want to generate the string
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// Create a buffer to store the random string
	randomString := make([]byte, length)

	// Generate random characters and append them to the buffer
	for i := 0; i < length; i++ {
		randomString[i] = charset[rand.Intn(len(charset))]
	}

	return string(randomString)
}

// make extranonce for miner.
func makeExtranonce(length int) string {
	// Define the characters from which you want to generate the string
	const charset = "abcdef0123456789"

	// Create a buffer to store the random string
	randomString := make([]byte, length)

	// Generate random characters and append them to the buffer
	for i := 0; i < length; i++ {
		randomString[i] = charset[rand.Intn(len(charset))]
	}

	return string(randomString)
}

// NewHeads send a notification each time a new (header) block is appended to the chain.
// The "user,pass,agent" parameter is provided solely for compatibility with standalone mining pool services,
// but it is not utilized here.
func (api *API) NewWork(ctx context.Context, user, pass, agent string) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	if api.ethash.remote == nil {
		return &rpc.Subscription{}, errors.New("remote mining not supported")
	}

	rpcSub := notifier.CreateSubscription()

	go func() {
		if api.ethash.remote.currentBlock != nil {
			// add jobId and extranonce
			var minerWork = make([]string, 6)
			minerWork[0] = makeJobId(5)
			copy(minerWork[1:], api.ethash.remote.currentWork[:])
			minerWork[5] = makeExtranonce(4)
			notifier.Notify(rpcSub.ID, minerWork)
		}

		workCh := make(chan [4]string, 1)

		for {
			select {
			case api.ethash.remote.notifyWorkCh <- workCh:
			case <-api.ethash.remote.exitCh:
				return
			}

			select {
			case work := <-workCh:
				// add jobId and extranonce
				var minerWork = make([]string, 6)
				minerWork[0] = makeJobId(5)
				copy(minerWork[1:], work[:])
				minerWork[5] = makeExtranonce(4)
				notifier.Notify(rpcSub.ID, minerWork)
			case <-rpcSub.Err():
				return
			case <-notifier.Closed():
				return
			}
		}
	}()

	return rpcSub, nil
}
