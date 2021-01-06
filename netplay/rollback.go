package netplay

import (
	"log"

	"github.com/libretro/ludo/input"
	"github.com/libretro/ludo/state"
)

var saved struct {
	GameState []byte
	Inputs    [input.MaxPlayers][input.MaxFrames]input.PlayerState
	Tick      int64
}

func serialize() {
	//log.Println("Serialize")
	s := state.Global.Core.SerializeSize()
	bytes, err := state.Global.Core.Serialize(s)
	if err != nil {
		log.Println(err)
	}
	saved.GameState = make([]byte, s)
	copy(saved.GameState[:], bytes[:])

	saved.Inputs = input.Serialize()
	saved.Tick = state.Global.Tick
}

func unserialize() {
	log.Println("Unserialize")
	if len(saved.GameState) == 0 {
		log.Println("Trying to unserialize a savestate of len 0")
		return
	}

	s := state.Global.Core.SerializeSize()
	err := state.Global.Core.Unserialize(saved.GameState, s)
	if err != nil {
		log.Println(err)
	}
	input.Unserialize(saved.Inputs)
	state.Global.Tick = saved.Tick
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// handleRollbacks will rollback if needed.
func handleRollbacks(gameUpdate func()) {
	lastGameTick := state.Global.Tick - 1
	// The input needed to resync state is available so rollback.
	// lastSyncedTick keeps track of the lastest synced game tick.
	// When the tick count for the inputs we have is more than the number of synced ticks it's possible to rerun those game updates
	// with a rollback.

	// The number of frames that's elasped since the game has been out of sync.
	// Rerun rollbackFrames number of updates.
	rollbackFrames := lastGameTick - lastSyncedTick

	wrongPrediction := false
	for i := lastSyncedTick; i <= min(lastGameTick, confirmedTick); i++ {
		//log.Println(i, encodeInput(getRemoteInputState(i)) != lastPrediction)
		if encodeInput(getRemoteInputState(i)) != lastPrediction {
			wrongPrediction = true
		}
	}

	// Update the graph indicating the number of rollback frames
	// rollbackGraphTable[ 1 + (lastGameTick % 60) * 2 + 1  ] = -1 * rollbackFrames * GRAPH_UNIT_SCALE

	if lastGameTick >= 0 && lastGameTick > (lastSyncedTick+1) && confirmedTick > lastSyncedTick {
		if wrongPrediction {
			log.Println("Rollback", rollbackFrames, "frames")
			state.Global.FastForward = true

			// Must revert back to the last known synced game frame.
			unserialize()

			for i := int64(0); i < rollbackFrames; i++ {
				// Get input from the input history buffer. The network system will predict input after the last confirmed tick (for the remote player).
				input.SetState(input.LocalPlayerPort, getLocalInputState(state.Global.Tick)) // Offset of 1 ensure it's used for the next game update.
				input.SetState(input.RemotePlayerPort, getRemoteInputState(state.Global.Tick))

				lastRolledBackGameTick := state.Global.Tick
				gameUpdate()
				state.Global.Tick++

				// Confirm that we are indeed still synced
				if lastRolledBackGameTick <= confirmedTick {
					log.Println("Save after rollback")
					serialize()

					lastSyncedTick = lastRolledBackGameTick

					// Confirm the game clients are in sync
					checkSync()
				}
			}

			state.Global.FastForward = false
		} else {
			lastSyncedTick = min(lastGameTick, confirmedTick)
			if lastSyncedTick == state.Global.Tick {
				serialize()
			}
		}
	}
}
