package core

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-hamt-ipld"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/go-filecoin/address"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	"github.com/filecoin-project/go-filecoin/types"
)

var mockSigner, _ = types.NewMockSignersAndKeyInfo(10)
var newSignedMessage = types.NewSignedMessageForTestGetter(mockSigner)

func TestMessagePoolAddRemove(t *testing.T) {
	assert := assert.New(t)
	ctx := context.Background()

	api := th.NewTestMessagePoolAPI(0)
	pool := NewMessagePool(api)
	msg1 := newSignedMessage()
	msg2 := setNonce(mockSigner, newSignedMessage(), 1)

	c1, err := msg1.Cid()
	assert.NoError(err)
	c2, err := msg2.Cid()
	assert.NoError(err)

	assert.Len(pool.Pending(), 0)
	m, ok := pool.Get(c1)
	assert.Nil(m)
	assert.False(ok)

	_, err = pool.Add(ctx, msg1)
	assert.NoError(err)
	assert.Len(pool.Pending(), 1)

	_, err = pool.Add(ctx, msg2)
	assert.NoError(err)
	assert.Len(pool.Pending(), 2)

	m, ok = pool.Get(c1)
	assert.Equal(msg1, m)
	assert.True(ok)
	m, ok = pool.Get(c2)
	assert.Equal(msg2, m)
	assert.True(ok)

	pool.Remove(c1)
	assert.Len(pool.Pending(), 1)
	pool.Remove(c2)
	assert.Len(pool.Pending(), 0)
}

func TestMessagePoolValidate(t *testing.T) {
	t.Run("message pool rejects messages after it reaches its limit", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)

		ctx := context.Background()
		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))

		// we need a lot of messages, which means a lot of signers to avoid the max nonce gap
		numSigners := MaxMessagePoolSize / (MaxNonceGap - 1)
		ki := types.MustGenerateKeyInfo(numSigners, types.GenerateBigKeyInfoSeed())
		signer := types.NewMockSigner(ki)

		msgGetter := types.NewMessageForTestGetter()
		for i := 0; i < MaxMessagePoolSize; i++ {
			msg := msgGetter()
			msg.From = signer.Addresses[i/MaxNonceGap]
			msg.Nonce = types.Uint64(i % MaxNonceGap)
			smsg, err := types.NewSignedMessage(*msg, signer, types.NewGasPrice(0), types.NewGasUnits(0))
			require.NoError(err)

			_, err = pool.Add(ctx, smsg)
			require.NoError(err)
		}

		assert.Len(pool.Pending(), MaxMessagePoolSize)

		// attempt to add one more
		msg := msgGetter()
		msg.From = signer.Addresses[len(signer.Addresses)-1]
		msg.Nonce = types.Uint64(MaxNonceGap)
		smsg, err := types.NewSignedMessage(*msg, signer, types.NewGasPrice(0), types.NewGasUnits(0))
		require.NoError(err)

		_, err = pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), ErrMessagePoolFull.Error())

		assert.Len(pool.Pending(), MaxMessagePoolSize)
	})

	t.Run("validates no two messages are added with same nonce", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)

		ctx := context.Background()
		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))

		smsg1 := newSignedMessage()
		_, err := pool.Add(ctx, smsg1)
		require.NoError(err)

		smsg2 := setNonce(mockSigner, newSignedMessage(), smsg1.Nonce)
		_, err = pool.Add(ctx, smsg2)
		require.Error(err)
		assert.Contains(err.Error(), ErrDuplicateNonce.Error())
	})

	t.Run("validates added nonce doesn't exceed current nonce by more than limit", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)

		ctx := context.Background()
		api := th.NewTestMessagePoolAPI(0)
		pool := NewMessagePool(api)

		currentNonce := types.Uint64(52)

		// setup api so that we find the actor
		api.ActorAddr = mockSigner.Addresses[0]
		api.Actor.Nonce = currentNonce

		smsg1 := setNonce(mockSigner, newSignedMessage(), 103)
		_, err := pool.Add(ctx, smsg1)
		require.NoError(err)

		smsg2 := setNonce(mockSigner, newSignedMessage(), currentNonce+MaxNonceGap+1)
		_, err = pool.Add(ctx, smsg2)

		require.Error(err)
		assert.Contains(err.Error(), ErrNonceGapExceeded.Error())
	})

	t.Run("message pool validates signatures", func(t *testing.T) {
		assert := assert.New(t)
		ctx := context.Background()

		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))
		smsg := newSignedMessage()
		smsg.Message.Nonce = types.Uint64(uint64(smsg.Message.Nonce) + uint64(1)) // invalidate message

		c, err := pool.Add(ctx, smsg)
		assert.False(c.Defined())
		assert.Error(err)

		c, _ = smsg.Cid()
		m, ok := pool.Get(c)
		assert.Nil(m)
		assert.False(ok)
	})

	t.Run("self send fails", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		smsg := newSignedMessage()
		smsg = resignMessage(mockSigner, smsg, func(msg *types.Message) {
			msg.To = msg.From
		})

		ctx := context.Background()
		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))

		_, err := pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "cannot send to self")
	})

	t.Run("validates from account actor", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		smsg := newSignedMessage()

		ctx := context.Background()
		api := th.NewTestMessagePoolAPI(0)
		pool := NewMessagePool(api)

		api.ActorAddr = smsg.From
		api.Actor.Code = types.MinerActorCodeCid

		_, err := pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "non-account actor")
	})

	t.Run("validates positive value", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		smsg := newSignedMessage()
		smsg = resignMessage(mockSigner, smsg, func(msg *types.Message) {
			msg.Value = types.NewAttoFIL(big.NewInt(-500000000))
		})

		ctx := context.Background()
		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))

		_, err := pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "negative value")
	})

	t.Run("validates block gas limit fails", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		msg := newSignedMessage()
		gasUnits := types.NewGasUnits(uint64(types.BlockGasLimit + 2000))
		smsg, err := types.NewSignedMessage(msg.Message, mockSigner, types.NewGasPrice(0), gasUnits)
		require.NoError(err)

		ctx := context.Background()
		pool := NewMessagePool(th.NewTestMessagePoolAPI(0))

		_, err = pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "above block gas limit")
	})

	t.Run("can't cover value", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		// message costs a maximum 20*1000 in gas
		msg := newSignedMessage()
		gasPrice := types.NewAttoFILFromFIL(20)
		gasUnits := types.NewGasUnits(uint64(1000))
		smsg, err := types.NewSignedMessage(msg.Message, mockSigner, *gasPrice, gasUnits)
		require.NoError(err)

		ctx := context.Background()
		api := th.NewTestMessagePoolAPI(0)
		pool := NewMessagePool(api)

		// give sender 1000 FIL
		api.ActorAddr = smsg.From
		api.Actor.Balance = types.NewAttoFILFromFIL(1000)

		_, err = pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "balance insufficient")
	})

	t.Run("low nonce", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		smsg := newSignedMessage()

		ctx := context.Background()
		api := th.NewTestMessagePoolAPI(0)
		pool := NewMessagePool(api)

		// give actor high nonce
		api.ActorAddr = smsg.From
		api.Actor.Nonce = 5

		_, err := pool.Add(ctx, smsg)
		require.Error(err)
		assert.Contains(err.Error(), "nonce too low")
	})
}

func TestMessagePoolDedup(t *testing.T) {
	assert := assert.New(t)
	ctx := context.Background()

	pool := NewMessagePool(th.NewTestMessagePoolAPI(0))
	msg1 := newSignedMessage()

	assert.Len(pool.Pending(), 0)
	_, err := pool.Add(ctx, msg1)
	assert.NoError(err)
	assert.Len(pool.Pending(), 1)

	_, err = pool.Add(ctx, msg1)
	assert.NoError(err)
	assert.Len(pool.Pending(), 1)
}

func TestMessagePoolAsync(t *testing.T) {
	assert := assert.New(t)
	ctx := context.Background()

	count := MaxNonceGap
	msgs := types.NewSignedMsgs(count, mockSigner)

	pool := NewMessagePool(th.NewTestMessagePoolAPI(0))
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			for j := 0; j < count/4; j++ {
				_, err := pool.Add(ctx, msgs[j+(count/4)*i])
				assert.NoError(err)
			}
			wg.Done()
		}(i)
	}

	wg.Wait()
	assert.Len(pool.Pending(), count)
}

func msgAsString(msg *types.SignedMessage) string {
	// When using NewMessageForTestGetter msg.Method is set
	// to "msgN" so we print that (it will correspond
	// to a variable of the same name in the tests
	// below).
	return msg.Message.Method
}

func msgsAsString(msgs []*types.SignedMessage) string {
	s := ""
	for _, m := range msgs {
		s = fmt.Sprintf("%s%s ", s, msgAsString(m))
	}
	return "[" + s + "]"
}

// assertPoolEquals returns true if p contains exactly the expected messages.
func assertPoolEquals(assert *assert.Assertions, p *MessagePool, expMsgs ...*types.SignedMessage) {
	msgs := p.Pending()
	if len(msgs) != len(expMsgs) {
		assert.Failf("wrong messages in pool", "expMsgs %v, got msgs %v", msgsAsString(expMsgs), msgsAsString(msgs))

	}
	for _, m1 := range expMsgs {
		found := false
		for _, m2 := range msgs {
			if types.SmsgCidsEqual(m1, m2) {
				found = true
				break
			}
		}
		if !found {
			assert.Failf("wrong messages in pool", "expMsgs %v, got msgs %v (msgs doesn't contain %v)", msgsAsString(expMsgs), msgsAsString(msgs), msgAsString(m1))
		}
	}
}

func headOf(chain []types.TipSet) types.TipSet {
	return chain[len(chain)-1]
}

func TestUpdateMessagePool(t *testing.T) {
	assert := assert.New(t)
	ctx := context.Background()
	type msgs []*types.SignedMessage
	type msgsSet [][]*types.SignedMessage

	t.Run("Replace head", func(t *testing.T) {
		// Msg pool: [m0, m1], Chain: b[]
		// to
		// Msg pool: [m0],     Chain: b[m1]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(2, mockSigner)
		MustAdd(p, m[0], m[1])

		parent := types.TipSet{}
		blk := types.Block{Height: 0}
		parent[blk.Cid()] = &blk

		oldChain := NewChainWithMessages(store, parent, msgsSet{})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, parent, msgsSet{msgs{m[1]}})
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[0])
	})

	t.Run("Replace head with self", func(t *testing.T) {
		// Msg pool: [m0, m1], Chain: b[m2]
		// to
		// Msg pool: [m0, m1], Chain: b[m2]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(3, mockSigner)
		MustAdd(p, m[0], m[1])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{m[2]}})
		oldTipSet := headOf(oldChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, oldTipSet)) // sic
		assertPoolEquals(assert, p, m[0], m[1])
	})

	t.Run("Replace head with a long chain", func(t *testing.T) {
		// Msg pool: [m2, m5],     Chain: b[m0, m1]
		// to
		// Msg pool: [m1],         Chain: b[m2, m3] -> b[m4] -> b[m0] -> b[] -> b[m5, m6]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(7, mockSigner)
		MustAdd(p, m[2], m[5])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{m[0], m[1]}})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[2], m[3]}},
			msgsSet{msgs{m[4]}},
			msgsSet{msgs{m[0]}},
			msgsSet{msgs{}},
			msgsSet{msgs{m[5], m[6]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[1])
	})

	t.Run("Replace head with multi-block tipset chains", func(t *testing.T) {
		// Msg pool: [m2, m5],     Chain: {b[m0], b[m1]}
		// to
		// Msg pool: [m1],         Chain: b[m2, m3] -> {b[m4], b[m0], b[], b[]} -> {b[], b[m6,m5]}
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(7, mockSigner)
		MustAdd(p, m[2], m[5])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{m[0]}, msgs{m[1]}})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[2], m[3]}},
			msgsSet{msgs{m[4]}, msgs{m[0]}, msgs{}, msgs{}},
			msgsSet{msgs{}, msgs{m[5], m[6]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[1])
	})

	t.Run("Replace internal node (second one)", func(t *testing.T) {
		// Msg pool: [m3, m5],     Chain: b[m0] -> b[m1] -> b[m2]
		// to
		// Msg pool: [m1, m2],     Chain: b[m0] -> b[m3] -> b[m4, m5]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(6, mockSigner)
		MustAdd(p, m[3], m[5])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{m[0]}}, msgsSet{msgs{m[1]}}, msgsSet{msgs{m[2]}})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, oldChain[0], msgsSet{msgs{m[3]}}, msgsSet{msgs{m[4], m[5]}})
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[1], m[2])
	})

	t.Run("Replace internal node (second one) with a long chain", func(t *testing.T) {
		// Msg pool: [m6],         Chain: b[m0] -> b[m1] -> b[m2]
		// to
		// Msg pool: [m6],         Chain: b[m0] -> b[m3] -> b[m4] -> b[m5] -> b[m1, m2]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(7, mockSigner)
		MustAdd(p, m[6])

		oldChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[0]}},
			msgsSet{msgs{m[1]}},
			msgsSet{msgs{m[2]}},
		)
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, oldChain[0],
			msgsSet{msgs{m[3]}},
			msgsSet{msgs{m[4]}},
			msgsSet{msgs{m[5]}},
			msgsSet{msgs{m[1], m[2]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[6])
	})

	t.Run("Replace internal node with multi-block tipset chains", func(t *testing.T) {
		// Msg pool: [m6],         Chain: {b[m0], b[m1]} -> b[m2]
		// to
		// Msg pool: [m6],         Chain: {b[m0], b[m1]} -> b[m3] -> b[m4] -> {b[m5], b[m1, m2]}
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(7, mockSigner)
		MustAdd(p, m[6])

		oldChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[0]}, msgs{m[1]}},
			msgsSet{msgs{m[2]}},
		)
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, oldChain[0],
			msgsSet{msgs{m[3]}},
			msgsSet{msgs{m[4]}},
			msgsSet{msgs{m[5]}, msgs{m[1], m[2]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[6])
	})

	t.Run("Replace with same messages in different block structure", func(t *testing.T) {
		// Msg pool: [m3, m5],     Chain: b[m0] -> b[m1] -> b[m2]
		// to
		// Msg pool: [m3, m5],     Chain: {b[m0], b[m1], b[m2]}
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(6, mockSigner)
		MustAdd(p, m[3], m[5])

		oldChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[0]}},
			msgsSet{msgs{m[1]}},
			msgsSet{msgs{m[2]}},
		)
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[0]}, msgs{m[1]}, msgs{m[2]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[3], m[5])
	})

	t.Run("Truncate to internal node", func(t *testing.T) {
		// Msg pool: [],               Chain: b[m0] -> b[m1] -> b[m2] -> b[m3]
		// to
		// Msg pool: [m2, m3],         Chain: b[m0] -> b[m1]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))
		m := types.NewSignedMsgs(4, mockSigner)

		oldChain := NewChainWithMessages(store, types.TipSet{},
			msgsSet{msgs{m[0]}},
			msgsSet{msgs{m[1]}},
			msgsSet{msgs{m[2]}},
			msgsSet{msgs{m[3]}},
		)
		oldTipSet := headOf(oldChain)

		oldTipSetPrev := oldChain[1]
		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, oldTipSetPrev))
		assertPoolEquals(assert, p, m[2], m[3])
	})

	t.Run("Extend head", func(t *testing.T) {
		// Msg pool: [m0, m1], Chain: b[]
		// to
		// Msg pool: [m0],     Chain: b[] -> b[m1, m2]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(3, mockSigner)
		MustAdd(p, m[0], m[1])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{}})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, oldChain[len(oldChain)-1], msgsSet{msgs{m[1], m[2]}})
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p, m[0])
	})

	t.Run("Extend head with a longer chain and more messages", func(t *testing.T) {
		// Msg pool: [m2, m5],     Chain: b[m0] -> b[m1]
		// to
		// Msg pool: [],           Chain: b[m0] -> b[m1] -> b[m2, m3] -> b[m4] -> b[m5, m6]
		store := hamt.NewCborStore()
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(7, mockSigner)
		MustAdd(p, m[2], m[5])

		oldChain := NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{m[0]}}, msgsSet{msgs{m[1]}})
		oldTipSet := headOf(oldChain)

		newChain := NewChainWithMessages(store, oldChain[1],
			msgsSet{msgs{m[2], m[3]}},
			msgsSet{msgs{m[4]}},
			msgsSet{msgs{m[5], m[6]}},
		)
		newTipSet := headOf(newChain)

		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, oldTipSet, newTipSet))
		assertPoolEquals(assert, p)
	})

	t.Run("Times out old messages", func(t *testing.T) {
		require := require.New(t)

		var err error
		store := hamt.NewCborStore()
		api := th.NewTestMessagePoolAPI(0)
		p := NewMessagePool(api)

		m := types.NewSignedMsgs(MessageTimeOut, mockSigner)

		head := headOf(NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{}}))

		// Add a message at each block height until MessageTimeOut is reached
		for i := 0; i < MessageTimeOut; i++ {
			// api.Height determines block time at which message is added
			api.Height, err = head.Height()
			require.NoError(err)

			MustAdd(p, m[i])

			// update pool with tipset that has no messages
			next := headOf(NewChainWithMessages(store, head, msgsSet{msgs{}}))
			assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, head, next))

			// assert all added messages still in pool
			assertPoolEquals(assert, p, m[:i+1]...)

			head = next
		}

		// next tipset times out first message only
		next := headOf(NewChainWithMessages(store, head, msgsSet{msgs{}}))
		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, head, next))
		assertPoolEquals(assert, p, m[1:]...)

		// adding a chain of multiple tipsets times out based on final state
		for i := 0; i < 4; i++ {
			next = headOf(NewChainWithMessages(store, next, msgsSet{msgs{}}))
		}
		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, head, next))
		assertPoolEquals(assert, p, m[5:]...)
	})

	t.Run("Message timeout is unaffected by null tipsets", func(t *testing.T) {
		require := require.New(t)

		var err error
		store := hamt.NewCborStore()
		blockTimer := th.NewTestMessagePoolAPI(0)
		p := NewMessagePool(blockTimer)

		m := types.NewSignedMsgs(MessageTimeOut, mockSigner)

		head := headOf(NewChainWithMessages(store, types.TipSet{}, msgsSet{msgs{}}))

		// Add a message at each block height until MessageTimeOut is reached
		for i := 0; i < MessageTimeOut; i++ {
			// blockTimer.Height determines block time at which message is added
			blockTimer.Height, err = head.Height()
			require.NoError(err)

			MustAdd(p, m[i])

			// update pool with tipset that has no messages
			height, err := head.Height()
			require.NoError(err)

			// create a tipset at given height with one block containing no messages
			next := types.TipSet{}
			nextHeight := types.Uint64(height + 5) // simulate 4 null blocks
			blk := &types.Block{
				Height:  nextHeight,
				Parents: head.ToSortedCidSet(),
			}
			MustPut(store, blk)
			next[blk.Cid()] = blk

			assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, head, next))

			// assert all added messages still in pool
			assertPoolEquals(assert, p, m[:i+1]...)

			head = next
		}

		// next tipset times out first message only
		next := headOf(NewChainWithMessages(store, head, msgsSet{msgs{}}))
		assert.NoError(p.UpdateMessagePool(ctx, &storeBlockProvider{store}, head, next))
		assertPoolEquals(assert, p, m[1:]...)
	})
}

func TestLargestNonce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	t.Run("No matches", func(t *testing.T) {
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewSignedMsgs(2, mockSigner)
		MustAdd(p, m[0], m[1])

		_, found := p.LargestNonce(address.NewForTestGetter()())
		assert.False(found)
	})

	t.Run("Match, largest is zero", func(t *testing.T) {
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewMsgsWithAddrs(1, mockSigner.Addresses)
		m[0].Nonce = 0

		sm, err := types.SignMsgs(mockSigner, m)
		require.NoError(err)

		MustAdd(p, sm...)

		largest, found := p.LargestNonce(m[0].From)
		assert.True(found)
		assert.Equal(uint64(0), largest)
	})

	t.Run("Match", func(t *testing.T) {
		p := NewMessagePool(th.NewTestMessagePoolAPI(0))

		m := types.NewMsgsWithAddrs(3, mockSigner.Addresses)
		m[1].Nonce = 1
		m[2].Nonce = 2
		m[2].From = m[1].From

		sm, err := types.SignMsgs(mockSigner, m)
		require.NoError(err)

		MustAdd(p, sm...)

		largest, found := p.LargestNonce(m[2].From)
		assert.True(found)
		assert.Equal(uint64(2), largest)
	})
}

type storeBlockProvider struct {
	store *hamt.CborIpldStore
}

func (p *storeBlockProvider) GetBlock(ctx context.Context, cid cid.Cid) (*types.Block, error) {
	var blk types.Block
	if err := p.store.Get(ctx, cid, &blk); err != nil {
		return nil, errors.Wrapf(err, "failed to get block %s", cid)
	}
	return &blk, nil
}

func setNonce(signer types.Signer, message *types.SignedMessage, nonce types.Uint64) *types.SignedMessage {
	return resignMessage(signer, message, func(m *types.Message) {
		m.Nonce = nonce
	})
}

func resignMessage(signer types.Signer, message *types.SignedMessage, f func(*types.Message)) *types.SignedMessage {
	var msg types.Message
	msg = message.Message
	f(&msg)
	smg, err := signMessage(signer, msg)
	if err != nil {
		panic("Error signing message")
	}
	return smg
}

func signMessage(signer types.Signer, message types.Message) (*types.SignedMessage, error) {
	return types.NewSignedMessage(message, signer, types.NewGasPrice(0), types.NewGasUnits(0))
}
