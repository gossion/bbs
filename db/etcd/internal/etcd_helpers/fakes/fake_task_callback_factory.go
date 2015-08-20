// This file was generated by counterfeiter
package fakes

import (
	"sync"

	"github.com/cloudfoundry-incubator/bbs/db"
	"github.com/cloudfoundry-incubator/bbs/db/etcd/internal/etcd_helpers"
	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/pivotal-golang/lager"
)

type FakeTaskCallbackFactory struct {
	TaskCallbackWorkStub        func(logger lager.Logger, taskDB db.TaskDB, task *models.Task) func()
	taskCallbackWorkMutex       sync.RWMutex
	taskCallbackWorkArgsForCall []struct {
		logger lager.Logger
		taskDB db.TaskDB
		task   *models.Task
	}
	taskCallbackWorkReturns struct {
		result1 func()
	}
}

func (fake *FakeTaskCallbackFactory) TaskCallbackWork(logger lager.Logger, taskDB db.TaskDB, task *models.Task) func() {
	fake.taskCallbackWorkMutex.Lock()
	fake.taskCallbackWorkArgsForCall = append(fake.taskCallbackWorkArgsForCall, struct {
		logger lager.Logger
		taskDB db.TaskDB
		task   *models.Task
	}{logger, taskDB, task})
	fake.taskCallbackWorkMutex.Unlock()
	if fake.TaskCallbackWorkStub != nil {
		return fake.TaskCallbackWorkStub(logger, taskDB, task)
	} else {
		return fake.taskCallbackWorkReturns.result1
	}
}

func (fake *FakeTaskCallbackFactory) TaskCallbackWorkCallCount() int {
	fake.taskCallbackWorkMutex.RLock()
	defer fake.taskCallbackWorkMutex.RUnlock()
	return len(fake.taskCallbackWorkArgsForCall)
}

func (fake *FakeTaskCallbackFactory) TaskCallbackWorkArgsForCall(i int) (lager.Logger, db.TaskDB, *models.Task) {
	fake.taskCallbackWorkMutex.RLock()
	defer fake.taskCallbackWorkMutex.RUnlock()
	return fake.taskCallbackWorkArgsForCall[i].logger, fake.taskCallbackWorkArgsForCall[i].taskDB, fake.taskCallbackWorkArgsForCall[i].task
}

func (fake *FakeTaskCallbackFactory) TaskCallbackWorkReturns(result1 func()) {
	fake.TaskCallbackWorkStub = nil
	fake.taskCallbackWorkReturns = struct {
		result1 func()
	}{result1}
}

var _ etcd_helpers.TaskCallbackFactory = new(FakeTaskCallbackFactory)