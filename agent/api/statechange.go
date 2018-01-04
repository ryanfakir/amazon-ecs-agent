// Copyright 2014-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package api

import (
	"fmt"
	"strconv"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/statechange"
	"github.com/pkg/errors"

	"github.com/aws/aws-sdk-go/aws"
)

// ContainerStateChange represents a state change that needs to be sent to the
// SubmitContainerStateChange API
type ContainerStateChange struct {
	// TaskArn is the unique identifier for the task
	TaskArn string
	// ContainerName is the name of the container
	ContainerName string
	// Status is the status to send
	Status ContainerStatus

	// Reason may contain details of why the container stopped
	Reason string
	// ExitCode is the exit code of the container, if available
	ExitCode *int
	// PortBindings are the details of the host ports picked for the specified
	// container ports
	PortBindings []PortBinding

	// Container is a pointer to the container involved in the state change that gives the event handler a hook into
	// storing what status was sent.  This is used to ensure the same event is handled only once.
	Container *Container
}

// TaskStateChange represents a state change that needs to be sent to the
// SubmitTaskStateChange API
type TaskStateChange struct {
	// Attachment is the eni attachment object to send
	Attachment *ENIAttachment
	// TaskArn is the unique identifier for the task
	TaskARN string
	// Status is the status to send
	Status TaskStatus
	// Reason may contain details of why the task stopped
	Reason string
	// Containers holds the events generated by containers owned by this task
	Containers []ContainerStateChange

	// PullStartedAt is the timestamp when the task start pulling
	PullStartedAt *time.Time
	// PullStoppedAt is the timestamp when the task finished pulling
	PullStoppedAt *time.Time
	// ExecutionStoppedAt is the timestamp when the essential container stopped
	ExecutionStoppedAt *time.Time

	// Task is a pointer to the task involved in the state change that gives the event handler a hook into storing
	// what status was sent.  This is used to ensure the same event is handled only once.
	Task *Task
}

// NewTaskStateChangeEvent creates a new task state change event
func NewTaskStateChangeEvent(task *Task, reason string) (TaskStateChange, error) {
	var event TaskStateChange
	taskKnownStatus := task.GetKnownStatus()
	if !taskKnownStatus.BackendRecognized() {
		return event, errors.Errorf(
			"create task state change event api: status not recognized by ECS: %v; task: %s",
			taskKnownStatus, task.Arn)
	}
	if task.GetSentStatus() >= taskKnownStatus {
		return event, errors.Errorf(
			"create task state change event api: status [%s] already sent for task %s",
			taskKnownStatus.String(), task.Arn)
	}

	event = TaskStateChange{
		TaskARN: task.Arn,
		Status:  taskKnownStatus,
		Reason:  reason,
		Task:    task,
	}

	event.SetTaskTimestamps()

	return event, nil
}

// NewContainerStateChangeEvent creates a new container state change event
func NewContainerStateChangeEvent(task *Task, cont *Container, reason string) (ContainerStateChange, error) {
	var event ContainerStateChange
	contKnownStatus := cont.GetKnownStatus()
	if !contKnownStatus.ShouldReportToBackend(cont.GetSteadyStateStatus()) {
		return event, errors.Errorf(
			"create container state change event api: status not recognized by ECS: %v; task: %s",
			contKnownStatus, task.Arn)
	}
	if cont.IsInternal() {
		return event, errors.Errorf(
			"create container state change event api: internal container: %s",
			cont.Name)
	}
	if cont.GetSentStatus() >= contKnownStatus {
		return event, errors.Errorf(
			"create container state change event api: status [%s] already sent for container %s, task %s",
			contKnownStatus.String(), cont.Name, task.Arn)
	}

	if reason == "" && cont.ApplyingError != nil {
		reason = cont.ApplyingError.Error()
	}
	event = ContainerStateChange{
		TaskArn:       task.Arn,
		ContainerName: cont.Name,
		Status:        contKnownStatus.BackendStatus(cont.GetSteadyStateStatus()),
		ExitCode:      cont.GetKnownExitCode(),
		PortBindings:  cont.KnownPortBindings,
		Reason:        reason,
		Container:     cont,
	}

	return event, nil
}

// String returns a human readable string representation of this object
func (c *ContainerStateChange) String() string {
	res := fmt.Sprintf("%s %s -> %s", c.TaskArn, c.ContainerName, c.Status.String())
	if c.ExitCode != nil {
		res += ", Exit " + strconv.Itoa(*c.ExitCode) + ", "
	}
	if c.Reason != "" {
		res += ", Reason " + c.Reason
	}
	if len(c.PortBindings) != 0 {
		res += fmt.Sprintf(", Ports %v", c.PortBindings)
	}
	if c.Container != nil {
		res += ", Known Sent: " + c.Container.GetSentStatus().String()
	}
	return res
}

// SetTaskTimestamps adds the timestamp information of task into the event
// to be sent by SubmitTaskStateChange
func (change *TaskStateChange) SetTaskTimestamps() {
	if change.Task == nil {
		return
	}

	// Send the task timestamp if set
	if timestamp := change.Task.GetPullStartedAt(); !timestamp.IsZero() {
		change.PullStartedAt = aws.Time(timestamp.UTC())
	}
	if timestamp := change.Task.GetPullStoppedAt(); !timestamp.IsZero() {
		change.PullStoppedAt = aws.Time(timestamp.UTC())
	}
	if timestamp := change.Task.GetExecutionStoppedAt(); !timestamp.IsZero() {
		change.ExecutionStoppedAt = aws.Time(timestamp.UTC())
	}
}

// ShouldBeReported checks if the statechange should be reported to backend
func (change *TaskStateChange) ShouldBeReported() bool {
	// Events that should be reported:
	// 1. Normal task state change: RUNNING/STOPPED
	// 2. Container state change, with task status in CREATED/RUNNING/STOPPED
	// The task timestamp will be sent in both of the event type
	// TODO Move the Attachment statechange check into this method
	if change.Status == TaskRunning || change.Status == TaskStopped {
		return true
	}

	if len(change.Containers) != 0 {
		return true
	}

	return false
}

// String returns a human readable string representation of this object
func (t *TaskStateChange) String() string {
	res := fmt.Sprintf("%s -> %s", t.TaskARN, t.Status.String())
	if t.Task != nil {
		res += fmt.Sprintf(", Known Sent: %s, PullStartedAt: %s, PullStoppedAt: %s, ExecutionStoppedAt: %s",
			t.Task.GetSentStatus().String(),
			t.Task.GetPullStartedAt(),
			t.Task.GetPullStoppedAt(),
			t.Task.GetExecutionStoppedAt())
	}
	if t.Attachment != nil {
		res += ", " + t.Attachment.String()
	}
	for _, containerChange := range t.Containers {
		res += ", " + containerChange.String()
	}

	return res
}

// GetEventType returns an enum identifying the event type
func (c ContainerStateChange) GetEventType() statechange.EventType {
	return statechange.ContainerEvent
}

// GetEventType returns an enum identifying the event type
func (t TaskStateChange) GetEventType() statechange.EventType {
	return statechange.TaskEvent
}
