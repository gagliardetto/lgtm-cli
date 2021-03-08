package main

import (
	"context"
	"sync"
	"time"

	"github.com/gagliardetto/eta"
	. "github.com/gagliardetto/utilz"
	"github.com/hako/durafmt"
	"golang.org/x/sync/semaphore"
)

type Unfollower struct {
	client *Client
	wg     *sync.WaitGroup
	sem    *semaphore.Weighted
}

func NewUnfollower(client *Client, maxWorkers int64) *Unfollower {
	return &Unfollower{
		client: client,
		wg:     &sync.WaitGroup{},
		sem:    semaphore.NewWeighted(maxWorkers),
	}
}

//
func (un *Unfollower) Unfollow(isProto bool, key string, name string, etac *eta.ETA) {
	if err := un.sem.Acquire(context.Background(), 1); err != nil {
		panic(err)
	}
	un.wg.Add(1)

	go un.unfollower(isProto, key, name, etac)
}

//
func (un *Unfollower) unfollower(isProto bool, key string, name string, etac *eta.ETA) {
	defer etac.Done(1)
	defer un.wg.Done()
	defer un.sem.Release(1)

	averagedETA := etac.GetETA()
	thisETA := durafmt.Parse(averagedETA.Round(time.Second)).String()

	Infof(
		"[%s](%v/%v) Unfollowing %s ... ETA %s",
		etac.GetFormattedPercentDone(),
		etac.GetDone()+1,
		etac.GetTotal(),
		name,
		thisETA,
	)

	unfollowFunc := un.client.UnfollowProject
	if isProto {
		unfollowFunc = un.client.UnfollowProtoProject
	}

	err := unfollowFunc(key)
	if err != nil {
		Errorf(
			"error while unfollowing project %s: %s",
			name,
			err,
		)
	} else {
		Successf(
			"[%s](%v/%v) Unfollowed %s; ETA %s",
			etac.GetFormattedPercentDone(),
			etac.GetDone()+1,
			etac.GetTotal(),
			name,
			thisETA,
		)
	}
}

func (un *Unfollower) Wait() error {
	un.wg.Wait()
	Errorln(LimeBG(">>> Completed. <<<"))
	return nil
}
