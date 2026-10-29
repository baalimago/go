// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

// wasix/wasm has no threads, as js/wasm: locks are flags, and a goroutine
// waiting on a note parks instead of blocking the only thread.

const (
	mutex_unlocked = 0
	mutex_locked   = 1

	note_cleared = 0
	note_woken   = 1
	note_timeout = 2

	active_spin     = 4
	active_spin_cnt = 30
	passive_spin    = 1
)

type mWaitList struct{}

func lockVerifyMSize() {}

func mutexContended(l *mutex) bool {
	return false
}

func lock(l *mutex) {
	lockWithRank(l, getLockRank(l))
}

func lock2(l *mutex) {
	if l.key == mutex_locked {
		// wasm is single-threaded so we should never
		// observe this.
		throw("self deadlock")
	}
	gp := getg()
	if gp.m.locks < 0 {
		throw("lock count")
	}
	gp.m.locks++
	l.key = mutex_locked
}

func unlock(l *mutex) {
	unlockWithRank(l)
}

func unlock2(l *mutex) {
	if l.key == mutex_unlocked {
		throw("unlock of unlocked lock")
	}
	gp := getg()
	gp.m.locks--
	if gp.m.locks < 0 {
		throw("lock count")
	}
	l.key = mutex_unlocked
}

// One-time notifications.

func noteclear(n *note) {
	n.status = note_cleared
}

func notewakeup(n *note) {
	if n.status == note_woken {
		throw("notewakeup - double wakeup")
	}
	cleared := n.status == note_cleared
	n.status = note_woken
	if cleared && n.gp != nil {
		goready(n.gp, 1)
	}
}

func notesleep(n *note) {
	throw("notesleep not supported on wasm")
}

func notetsleep(n *note, ns int64) bool {
	throw("notetsleep not supported on wasm")
	return false
}

// noteTimeout is the timer callback of a notetsleepg with a deadline.
func noteTimeout(arg any, _ uintptr, _ int64) {
	n := arg.(*note)
	if n.status == note_cleared && n.gp != nil {
		n.status = note_timeout
		goready(n.gp, 0)
	}
}

// same as runtime·notetsleep, but called on user g (not g0)
func notetsleepg(n *note, ns int64) bool {
	gp := getg()
	if gp == gp.m.g0 {
		throw("notetsleepg on g0")
	}
	if n.status != note_cleared {
		return n.status == note_woken
	}
	var t *timer
	if ns >= 0 {
		t = new(timer)
		t.init(noteTimeout, n)
		t.reset(nanotime()+ns, 0)
	}
	for n.status == note_cleared {
		n.gp = gp
		gopark(nil, nil, waitReasonSleep, traceBlockSleep, 1)
		n.gp = nil
	}
	if t != nil {
		t.stop()
	}
	return n.status == note_woken
}

func beforeIdle(int64, int64) (*g, bool) {
	return nil, false
}

func checkTimeouts() {}
