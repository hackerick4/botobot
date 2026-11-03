package cli

import (
	"sync/atomic"
	"time"
)

// sleptNS counts the time release would have waited in tests.
var sleptNS atomic.Int64

func testSleep(d time.Duration) { sleptNS.Add(int64(d)) }
