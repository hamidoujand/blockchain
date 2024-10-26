package worker

import "time"

// CORE NOTE: The POA mining operation is managed by this function which runs on
// it's own goroutine. The node starts a loop that is on a 12 second timer. At
// the beginning of each cycle the selection algorithm is executed which determines
// if this node needs to mine the next block. If this node is not selected, it
// waits for the next cycle to check the selection algorithm again.

// cycleDuration sets the mining operation to happen every 12 seconds
const secondsPerCycle = 12
const cycleDuration = secondsPerCycle * time.Second

// poaOperations handles mining.
func (w *Worker) poaOperations() {
	w.evHandler("worker: poaOperations: G started")
	defer w.evHandler("worker: poaOperations: G completed")
}
