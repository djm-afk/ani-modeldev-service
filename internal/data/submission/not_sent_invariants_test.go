package submission_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/biz"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/data/submission"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/internal/testsupport/postgres"
)

func TestNotSentThenUncertainThenConfirmedRetainsFirstObservations(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := validDispatchRequest(t, openPool())
	writerPool := openPool()
	first := reserveUncertaintyAttempt(t, ctx, writerPool, request)
	repository := submission.New(writerPool)
	permit := *first.SendPermit
	notSentAt := first.Dispatch.ReservedAt.Add(2 * time.Microsecond)
	uncertainAt := first.Dispatch.ReservedAt.Add(4 * time.Microsecond)
	confirmedAt := first.Dispatch.ReservedAt.Add(6 * time.Microsecond)
	want := first.Dispatch
	want.OwnerRevision = 3
	want.State, want.NotSentAt = biz.PipelineDispatchNotSent, &notSentAt
	got, err := repository.MarkSubmissionNotSent(ctx, permit, notSentAt)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("first local no-send observation changed original facts: %v", err)
	}
	want.State, want.UncertainAt = biz.PipelineDispatchUncertain, &uncertainAt
	want.OwnerRevision = 4
	got, err = repository.MarkSubmissionUncertain(ctx, permit, uncertainAt)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("uncertainty lost the original no-send time or attempt: %v", err)
	}
	want = expectedConfirmation(want, 5, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: confirmedAt})
	confirmed, err := repository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), confirmedAt)
	if err != nil || confirmed.ConflictingRuns || !reflect.DeepEqual(confirmed.Dispatch, want) {
		t.Fatalf("confirmation lost either earlier observation or its Run: %v", err)
	}
	// Earlier and later valid observations are replays, not timestamp updates.
	// Each first committed fact survives the later stronger summary state.
	for _, replayAt := range []time.Time{first.Dispatch.ReservedAt, first.Dispatch.ReservedAt.Add(20 * time.Microsecond)} {
		got, err = repository.MarkSubmissionNotSent(ctx, permit, replayAt)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("no-send replay refreshed its first time or downgraded confirmation: %v", err)
		}
		got, err = repository.MarkSubmissionUncertain(ctx, permit, replayAt)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("uncertainty replay erased no-send/Run facts or changed its first time: %v", err)
		}
		confirmed, err = repository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), replayAt)
		if err != nil || confirmed.ConflictingRuns || !reflect.DeepEqual(confirmed.Dispatch, want) {
			t.Fatalf("confirmed replay refreshed its first time or discarded history: %v", err)
		}
	}
	writerPool.Close()
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
}

func TestNotSentAfterStrongerObservationKeepsStateAndFirstTimes(t *testing.T) {
	for _, confirmedFirst := range []bool{false, true} {
		name := "uncertain first"
		if confirmedFirst {
			name = "confirmed first"
		}
		t.Run(name, func(t *testing.T) {
			openPool := postgres.Prepare(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := validDispatchRequest(t, openPool())
			writerPool := openPool()
			first := reserveUncertaintyAttempt(t, ctx, writerPool, request)
			repository := submission.New(writerPool)
			permit := *first.SendPermit
			strongerAt := first.Dispatch.ReservedAt.Add(4 * time.Microsecond)
			// A later delivery may carry an earlier valid observation time.
			notSentAt := first.Dispatch.ReservedAt.Add(2 * time.Microsecond)
			want := first.Dispatch
			want.OwnerRevision = 3
			if confirmedFirst {
				want = expectedConfirmation(want, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: strongerAt})
				got, err := repository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), strongerAt)
				if err != nil || got.ConflictingRuns || !reflect.DeepEqual(got.Dispatch, want) {
					t.Fatalf("first confirmation did not retain the original Run: %v", err)
				}
			} else {
				want.State, want.UncertainAt = biz.PipelineDispatchUncertain, &strongerAt
				got, err := repository.MarkSubmissionUncertain(ctx, permit, strongerAt)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("first uncertainty did not retain the original attempt: %v", err)
				}
			}
			want.NotSentAt = &notSentAt
			want.OwnerRevision = 4
			got, err := repository.MarkSubmissionNotSent(ctx, permit, notSentAt)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("late no-send observation downgraded or erased stronger facts: %v", err)
			}
			for _, replayAt := range []time.Time{first.Dispatch.ReservedAt, first.Dispatch.ReservedAt.Add(20 * time.Microsecond)} {
				got, err = repository.MarkSubmissionNotSent(ctx, permit, replayAt)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("early/late no-send replay refreshed its first time: %v", err)
				}
				if confirmedFirst {
					receipt, err := repository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), replayAt)
					if err != nil || receipt.ConflictingRuns || !reflect.DeepEqual(receipt.Dispatch, want) {
						t.Fatalf("confirmed replay lost no-send history or refreshed its Run time: %v", err)
					}
				} else {
					got, err = repository.MarkSubmissionUncertain(ctx, permit, replayAt)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("uncertainty replay lost no-send history or refreshed its first time: %v", err)
					}
				}
			}
			writerPool.Close()
			assertDispatchReplay(t, ctx, submission.New(openPool()), request, want)
		})
	}
}

func TestConcurrentNotSentAndConfirmedRetainBothOriginalFacts(t *testing.T) {
	openPool := postgres.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := validDispatchRequest(t, openPool())
	seedPool := openPool()
	first := reserveUncertaintyAttempt(t, ctx, seedPool, request)
	seedPool.Close()
	notSentPool, confirmedPool := openPool(), openPool()
	notSentRepository, confirmedRepository := submission.New(notSentPool), submission.New(confirmedPool)
	permit := *first.SendPermit
	notSentAt := first.Dispatch.ReservedAt.Add(time.Microsecond)
	confirmedAt := first.Dispatch.ReservedAt.Add(2 * time.Microsecond)
	wantNotSent := first.Dispatch
	wantNotSent.OwnerRevision = 3
	wantNotSent.State, wantNotSent.NotSentAt = biz.PipelineDispatchNotSent, &notSentAt
	wantConfirmed := expectedConfirmation(first.Dispatch, 3, biz.PipelineConfirmedRun{RunID: confirmedRunID, FirstObservedAt: confirmedAt})
	wantBoth := wantConfirmed
	wantBoth.OwnerRevision = 4
	wantBoth.NotSentAt = &notSentAt
	type result struct {
		notSent         bool
		dispatch        biz.PipelineDispatch
		conflictingRuns bool
		err             error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	go func() {
		<-start
		dispatch, err := notSentRepository.MarkSubmissionNotSent(ctx, permit, notSentAt)
		results <- result{notSent: true, dispatch: dispatch, err: err}
	}()
	go func() {
		<-start
		receipt, err := confirmedRepository.RecordSubmissionConfirmed(ctx, permit, confirmedObservation(confirmedRunID), confirmedAt)
		results <- result{dispatch: receipt.Dispatch, conflictingRuns: receipt.ConflictingRuns, err: err}
	}()
	close(start)
	combinedReceipts := 0
	for range 2 {
		var got result
		select {
		case got = <-results:
		case <-ctx.Done():
			t.Fatal("bounded no-send/confirmation race did not finish")
		}
		if got.err != nil || got.conflictingRuns {
			t.Fatalf("original no-send/confirmation race failed or invented another Run: %v", got.err)
		}
		if reflect.DeepEqual(got.dispatch, wantBoth) {
			combinedReceipts++
		} else if got.notSent {
			if !reflect.DeepEqual(got.dispatch, wantNotSent) {
				t.Fatal("first no-send receipt changed original facts or fabricated a Run")
			}
		} else if !reflect.DeepEqual(got.dispatch, wantConfirmed) {
			t.Fatal("first confirmation receipt changed its original Run or attempt")
		}
	}
	if combinedReceipts != 1 {
		t.Fatal("second serialized observer did not retain both committed facts")
	}
	notSentPool.Close()
	confirmedPool.Close()
	assertDispatchReplay(t, ctx, submission.New(openPool()), request, wantBoth)
}
