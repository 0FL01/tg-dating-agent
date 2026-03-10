package dating

import (
	"sync"
	"testing"
	"time"
)

func TestStateMachinePauseLifecycle(t *testing.T) {
	sm := NewStateMachine()

	paused, resumed, until := sm.CheckPause(time.Now())
	if paused || resumed || !until.IsZero() {
		t.Fatalf("initial CheckPause() = (%v, %v, %v), want (false, false, zero)", paused, resumed, until)
	}

	pausedUntil := sm.PauseFor(2 * time.Minute)
	if !sm.pausedUntil.Equal(pausedUntil) {
		t.Fatalf("pausedUntil field = %v, want %v", sm.pausedUntil, pausedUntil)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil.Add(-time.Nanosecond))
	if !paused || resumed || !until.Equal(pausedUntil) {
		t.Fatalf("CheckPause(before) = (%v, %v, %v), want (true, false, %v)", paused, resumed, until, pausedUntil)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil)
	if paused || !resumed || !until.IsZero() {
		t.Fatalf("CheckPause(at deadline) = (%v, %v, %v), want (false, true, zero)", paused, resumed, until)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil.Add(time.Nanosecond))
	if paused || resumed || !until.IsZero() {
		t.Fatalf("CheckPause(after clear) = (%v, %v, %v), want (false, false, zero)", paused, resumed, until)
	}
}

func TestStateMachinePauseForOverwritesExistingPause(t *testing.T) {
	sm := NewStateMachine()

	first := sm.PauseFor(1 * time.Second)
	second := sm.PauseFor(1 * time.Hour)

	if !sm.pausedUntil.Equal(second) {
		t.Fatalf("pausedUntil field = %v, want %v", sm.pausedUntil, second)
	}

	if !second.After(first) {
		t.Fatalf("second pause deadline = %v, want after first = %v", second, first)
	}
}

func TestStateMachineOwnProfileSkipExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(1000, 0)

	sm.MarkOwnProfileSkip(100, now)

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(ownProfileSkipTTL+time.Nanosecond)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after TTL expiry, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(ownProfileSkipTTL+2*time.Nanosecond)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after context expired and cleared, want false")
	}
}

func TestStateMachineOwnProfileSkipInterleavingWrongFirstMedia(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(2000, 0)

	sm.MarkOwnProfileSkip(100, now)

	if got := sm.ConsumeOwnProfileSkip(110, now.Add(time.Second)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true for non-correlated media, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(2*time.Second)); !got {
		t.Fatal("ConsumeOwnProfileSkip() = false for correlated own profile media, want true")
	}

	if got := sm.ConsumeOwnProfileSkip(102, now.Add(3*time.Second)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after successful consume, want false")
	}
}

func TestStateMachineEnqueueRejectsAfterStopAcceptingWork(t *testing.T) {
	sm := NewStateMachine()

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); !ok {
		t.Fatal("initial Enqueue() = false, want true")
	}

	sm.StopAcceptingWork()

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); ok {
		t.Fatal("Enqueue() after StopAcceptingWork = true, want false")
	}
}

func TestStateMachineEnqueueRejectsAfterStopAcceptingWorkConcurrently(t *testing.T) {
	sm := NewStateMachine()
	sm.StopAcceptingWork()

	const workers = 32
	var wg sync.WaitGroup
	successes := make(chan struct{}, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sm.Enqueue(ProfileJob{Type: "message"}) {
				successes <- struct{}{}
			}
		}()
	}

	wg.Wait()
	close(successes)

	if len(successes) != 0 {
		t.Fatalf("successful Enqueue() count = %d, want 0", len(successes))
	}
}

func TestStateMachineEnqueueDeduplicatesRecoveryJobsUntilDequeued(t *testing.T) {
	sm := NewStateMachine()

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); !ok {
		t.Fatal("first Enqueue(menu_recovery) = false, want true")
	}

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); ok {
		t.Fatal("second Enqueue(menu_recovery) = true, want false")
	}

	sm.OnJobDequeued("menu_recovery")

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); !ok {
		t.Fatal("Enqueue(menu_recovery) after dequeue = false, want true")
	}
}

func TestStateMachineStuckRecoveryEscalationCyclesAndResets(t *testing.T) {
	sm := NewStateMachine()

	if got := sm.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("NextStuckRecoveryEscalation() first = %d, want 1", got)
	}
	if got := sm.NextStuckRecoveryEscalation(); got != 2 {
		t.Fatalf("NextStuckRecoveryEscalation() second = %d, want 2", got)
	}
	if got := sm.NextStuckRecoveryEscalation(); got != 3 {
		t.Fatalf("NextStuckRecoveryEscalation() third = %d, want 3", got)
	}
	if got := sm.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("NextStuckRecoveryEscalation() after level 3 reset = %d, want 1", got)
	}

	sm.ResetStuckRecoveryEscalation()
	if got := sm.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("NextStuckRecoveryEscalation() after explicit reset = %d, want 1", got)
	}
}

func TestStateMachineBeginShutdownAtomicallyStopsAndRejectsEnqueue(t *testing.T) {
	sm := NewStateMachine()
	sm.MarkOwnProfileSkip(123, time.Now())

	sm.BeginShutdown()

	if got := sm.GetState(); got != StateStopped {
		t.Fatalf("state after BeginShutdown() = %v, want %v", got, StateStopped)
	}

	if sm.acceptingWork {
		t.Fatal("acceptingWork after BeginShutdown() = true, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(124, time.Now()); got {
		t.Fatal("own-profile skip context still active after BeginShutdown()")
	}

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); ok {
		t.Fatal("Enqueue() after BeginShutdown() = true, want false")
	}
}

func TestStateMachineGroupedCaptionConsumeLifecycle(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(3000, 0)

	sm.RememberGroupedCaption(777, "caption text", 10, now)

	got, ok := sm.ConsumeGroupedCaption(777, now.Add(time.Second))
	if !ok {
		t.Fatal("ConsumeGroupedCaption() ok = false, want true")
	}
	if got != "caption text" {
		t.Fatalf("ConsumeGroupedCaption() text = %q, want %q", got, "caption text")
	}

	if _, ok := sm.ConsumeGroupedCaption(777, now.Add(2*time.Second)); ok {
		t.Fatal("ConsumeGroupedCaption() ok = true after consume, want false")
	}
}

func TestStateMachineGroupedCaptionExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(4000, 0)

	sm.RememberGroupedCaption(888, "stale caption", 11, now)

	if got, ok := sm.ConsumeGroupedCaption(888, now.Add(groupedCaptionTTL+time.Nanosecond)); ok || got != "" {
		t.Fatalf("ConsumeGroupedCaption() = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestStateMachineStartupOwnProfileSkipConsumeLifecycle(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(5000, 0)

	sm.ArmStartupOwnProfileSkip(now)

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(time.Second)); !got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = false, want true")
	}

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(2 * time.Second)); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after consume, want false")
	}
}

func TestStateMachineStartupOwnProfileSkipExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(6000, 0)

	sm.ArmStartupOwnProfileSkip(now)

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(startupOwnProfileSkipTTL + time.Nanosecond)); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after TTL, want false")
	}
}

func TestStateMachineStartupOwnProfileSkipClearedExplicitly(t *testing.T) {
	sm := NewStateMachine()

	sm.ArmStartupOwnProfileSkip(time.Now())
	sm.ClearStartupOwnProfileSkip()

	if got := sm.ConsumeStartupOwnProfileSkip(time.Now()); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after Clear, want false")
	}
}

func TestStateMachineTryMarkProfileJobProcessingRejectsStaleAndDuplicate(t *testing.T) {
	sm := NewStateMachine()

	if ok := sm.Enqueue(ProfileJob{Type: "message", ProfileMessageID: 101}); !ok {
		t.Fatal("Enqueue(id=101) = false, want true")
	}
	if ok := sm.Enqueue(ProfileJob{Type: "message", ProfileMessageID: 105}); !ok {
		t.Fatal("Enqueue(id=105) = false, want true")
	}

	if accepted, latest, last := sm.TryMarkProfileJobProcessing(101); accepted {
		t.Fatalf("TryMarkProfileJobProcessing(101) accepted=true, want false (latest=%d last=%d)", latest, last)
	}

	if accepted, _, _ := sm.TryMarkProfileJobProcessing(105); !accepted {
		t.Fatal("TryMarkProfileJobProcessing(105) accepted=false, want true")
	}

	if accepted, latest, last := sm.TryMarkProfileJobProcessing(105); accepted {
		t.Fatalf("TryMarkProfileJobProcessing(105 duplicate) accepted=true, want false (latest=%d last=%d)", latest, last)
	}
}

func TestStateMachineHasPendingFresherProfileJob(t *testing.T) {
	sm := NewStateMachine()

	if pending, latest, last := sm.HasPendingFresherProfileJob(); pending {
		t.Fatalf("HasPendingFresherProfileJob() pending=true, want false (latest=%d last=%d)", latest, last)
	}

	if ok := sm.Enqueue(ProfileJob{Type: "message", ProfileMessageID: 205}); !ok {
		t.Fatal("Enqueue(id=205) = false, want true")
	}

	if pending, latest, last := sm.HasPendingFresherProfileJob(); !pending {
		t.Fatalf("HasPendingFresherProfileJob() pending=false, want true (latest=%d last=%d)", latest, last)
	}

	if accepted, _, _ := sm.TryMarkProfileJobProcessing(205); !accepted {
		t.Fatal("TryMarkProfileJobProcessing(205) accepted=false, want true")
	}

	if pending, latest, last := sm.HasPendingFresherProfileJob(); pending {
		t.Fatalf("HasPendingFresherProfileJob() pending=true after processing, want false (latest=%d last=%d)", latest, last)
	}
}

func TestStateMachineProfileLLMCacheSetGet(t *testing.T) {
	sm := NewStateMachine()

	sm.SetProfileLLMCache("profile-1", "INTJ", "Hi")

	mbti, opener, ok := sm.GetProfileLLMCache("profile-1")
	if !ok {
		t.Fatal("GetProfileLLMCache(profile-1) ok=false, want true")
	}
	if mbti != "INTJ" || opener != "Hi" {
		t.Fatalf("GetProfileLLMCache(profile-1) = (%q, %q), want (%q, %q)", mbti, opener, "INTJ", "Hi")
	}
}

func TestStateMachineProfileLLMCacheOverwriteKey(t *testing.T) {
	sm := NewStateMachine()

	sm.SetProfileLLMCache("profile-1", "INTJ", "Hi")
	sm.SetProfileLLMCache("profile-1", "INFJ", "Hello")

	mbti, opener, ok := sm.GetProfileLLMCache("profile-1")
	if !ok {
		t.Fatal("GetProfileLLMCache(profile-1) ok=false, want true")
	}
	if mbti != "INFJ" || opener != "Hello" {
		t.Fatalf("GetProfileLLMCache(profile-1) = (%q, %q), want (%q, %q)", mbti, opener, "INFJ", "Hello")
	}
}

func TestStateMachineProfileLLMCacheEvictsOldestAtLimit(t *testing.T) {
	sm := NewStateMachine()
	sm.profileLLMCacheMax = 2

	sm.SetProfileLLMCache("profile-1", "INTJ", "one")
	sm.SetProfileLLMCache("profile-2", "INFJ", "two")
	sm.SetProfileLLMCache("profile-3", "ENTJ", "three")

	if _, _, ok := sm.GetProfileLLMCache("profile-1"); ok {
		t.Fatal("GetProfileLLMCache(profile-1) ok=true after overflow, want false")
	}

	if mbti, opener, ok := sm.GetProfileLLMCache("profile-2"); !ok || mbti != "INFJ" || opener != "two" {
		t.Fatalf("GetProfileLLMCache(profile-2) = (%q, %q, %v), want (%q, %q, true)", mbti, opener, ok, "INFJ", "two")
	}

	if mbti, opener, ok := sm.GetProfileLLMCache("profile-3"); !ok || mbti != "ENTJ" || opener != "three" {
		t.Fatalf("GetProfileLLMCache(profile-3) = (%q, %q, %v), want (%q, %q, true)", mbti, opener, ok, "ENTJ", "three")
	}
}

func TestStateMachineProfileLLMCacheConcurrentReadWrite(t *testing.T) {
	sm := NewStateMachine()

	const workers = 16
	const iterations = 200

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				key := "shared-profile"
				if j%3 == 0 {
					key = "profile-" + string(rune('a'+(workerID%4)))
				}
				sm.SetProfileLLMCache(key, "INTJ", "hello")
				sm.GetProfileLLMCache(key)
			}
		}(i)
	}

	wg.Wait()

	if _, _, ok := sm.GetProfileLLMCache("shared-profile"); !ok {
		t.Fatal("GetProfileLLMCache(shared-profile) ok=false, want true")
	}
}

func TestStateMachineRecentReciprocalLikeContextStoreAndGet(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(7000, 0)

	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
		ProfileText: "bio-1",
		OpenerText:  "opener-1",
		MBTI:        "INTJ",
		Fingerprint: "fp-1",
		CapturedAt:  now,
	})
	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
		ProfileText: "bio-2",
		OpenerText:  "opener-2",
		MBTI:        "INFJ",
		Fingerprint: "fp-2",
		CapturedAt:  now.Add(time.Second),
	})

	latest, ok := sm.GetLatestReciprocalLikeContext(now.Add(2 * time.Second))
	if !ok {
		t.Fatal("GetLatestReciprocalLikeContext() ok=false, want true")
	}
	if latest.ProfileText != "bio-2" || latest.OpenerText != "opener-2" || latest.MBTI != "INFJ" || latest.Fingerprint != "fp-2" {
		t.Fatalf("latest reciprocal-like context = %+v, want bio-2/opener-2/INFJ/fp-2", latest)
	}

	list := sm.ListRecentReciprocalLikeContexts(now.Add(2*time.Second), -1)
	if len(list) != 2 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d, want 2", len(list))
	}
	if list[0].ProfileText != "bio-1" || list[1].ProfileText != "bio-2" {
		t.Fatalf("ListRecentReciprocalLikeContexts() order = [%q, %q], want [bio-1, bio-2]", list[0].ProfileText, list[1].ProfileText)
	}

	sm.ClearRecentReciprocalLikeContexts()
	if got := sm.ListRecentReciprocalLikeContexts(now.Add(2*time.Second), -1); len(got) != 0 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len after clear=%d, want 0", len(got))
	}
}

func TestStateMachineRecentReciprocalLikeContextTTLExpiryLazyPrune(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(8000, 0)

	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
		ProfileText: "stale",
		OpenerText:  "stale-opener",
		CapturedAt:  now,
	})

	if _, ok := sm.GetLatestReciprocalLikeContext(now.Add(reciprocalLikeContextTTL + time.Nanosecond)); ok {
		t.Fatal("GetLatestReciprocalLikeContext() ok=true after TTL expiry, want false")
	}

	if list := sm.ListRecentReciprocalLikeContexts(now.Add(reciprocalLikeContextTTL+2*time.Nanosecond), -1); len(list) != 0 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d after lazy prune, want 0", len(list))
	}
}

func TestStateMachineRecentReciprocalLikeContextEvictsOldestAtLimit(t *testing.T) {
	sm := NewStateMachine()
	sm.reciprocalLikeMax = 2
	now := time.Unix(9000, 0)

	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{ProfileText: "bio-1", CapturedAt: now})
	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{ProfileText: "bio-2", CapturedAt: now.Add(time.Second)})
	sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{ProfileText: "bio-3", CapturedAt: now.Add(2 * time.Second)})

	list := sm.ListRecentReciprocalLikeContexts(now.Add(3*time.Second), -1)
	if len(list) != 2 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d, want 2", len(list))
	}
	if list[0].ProfileText != "bio-2" || list[1].ProfileText != "bio-3" {
		t.Fatalf("eviction result = [%q, %q], want [bio-2, bio-3]", list[0].ProfileText, list[1].ProfileText)
	}
}

func TestStateMachineRecentReciprocalLikeContextConcurrentAccess(t *testing.T) {
	sm := NewStateMachine()
	sm.reciprocalLikeMax = 32

	const workers = 16
	const iterations = 200

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				now := time.Now()
				sm.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
					ProfileText: "profile",
					OpenerText:  "opener",
					MBTI:        "INTJ",
					Fingerprint: "worker",
					CapturedAt:  now.Add(time.Duration(workerID+j) * time.Millisecond),
				})
				sm.GetLatestReciprocalLikeContext(now)
				sm.ListRecentReciprocalLikeContexts(now, 5)
			}
		}(i)
	}

	wg.Wait()

	list := sm.ListRecentReciprocalLikeContexts(time.Now(), -1)
	if len(list) == 0 {
		t.Fatal("ListRecentReciprocalLikeContexts() len=0 after concurrent writes, want >0")
	}
	if len(list) > sm.reciprocalLikeMax {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d, want <=%d", len(list), sm.reciprocalLikeMax)
	}
}
