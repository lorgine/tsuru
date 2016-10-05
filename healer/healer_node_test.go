// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package healer

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/event"
	"github.com/tsuru/tsuru/event/eventtest"
	"github.com/tsuru/tsuru/iaas"
	iaasTesting "github.com/tsuru/tsuru/iaas/testing"
	"github.com/tsuru/tsuru/log"
	"github.com/tsuru/tsuru/permission"
	"github.com/tsuru/tsuru/provision"
	"github.com/tsuru/tsuru/provision/provisiontest"
	"gopkg.in/check.v1"
	"gopkg.in/mgo.v2/bson"
)

func (s *S) TestHealerHealNode(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	created, err := healer.healNode(nodes[0])
	c.Assert(err, check.IsNil)
	c.Assert(created.Address, check.Equals, "http://addr2:2")
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr2:2")

	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr2")
}

func (s *S) TestHealerHealNodeWithoutIaaS(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	err := p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{},
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Second,
	})
	healer.Shutdown()
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	created, err := healer.healNode(nodes[0])
	c.Assert(err, check.ErrorMatches, ".*error creating new machine.*")
	c.Assert(created, check.IsNil)
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	c.Assert(nodes[0].Status(), check.Equals, "enabled")
}

func (s *S) TestHealerHealNodeCreateMachineError(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	iaasInst.Err = fmt.Errorf("my create machine error")

	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	fakeNode := nodes[0].(*provisiontest.FakeNode)
	fakeNode.SetHealth(1, false)
	c.Assert(fakeNode.FailureCount() > 0, check.Equals, true)
	created, err := healer.healNode(nodes[0])
	c.Assert(err, check.ErrorMatches, ".*my create machine error.*")
	c.Assert(created, check.IsNil)
	c.Assert(fakeNode.FailureCount(), check.Equals, 0)
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	c.Assert(nodes[0].Status(), check.Equals, "enabled")
}

func (s *S) TestHealerHealNodeWaitAndRegisterError(c *check.C) {
	iaas.RegisterIaasProvider("my-healer-iaas", iaasTesting.NewHealerIaaSConstructor("addr1", nil))
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaas.RegisterIaasProvider("my-healer-iaas", iaasTesting.NewHealerIaaSConstructor("addr2", nil))
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)
	p.PrepareFailure("AddNode", fmt.Errorf("add node error"))
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:        p,
		WaitTimeNewMachine: time.Second,
	})
	healer.Shutdown()
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	created, err := healer.healNode(nodes[0])
	c.Assert(err, check.ErrorMatches, ".*error registering new node: add node error.*")
	c.Assert(created, check.IsNil)
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	c.Assert(nodes[0].Status(), check.Equals, "enabled")
}

func (s *S) TestHealerHealNodeDestroyError(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaasInst.DelErr = fmt.Errorf("my destroy error")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:        p,
		WaitTimeNewMachine: time.Minute,
	})
	healer.Shutdown()
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	buf := bytes.Buffer{}
	log.SetLogger(log.NewWriterLogger(&buf, false))
	defer log.SetLogger(nil)
	created, err := healer.healNode(nodes[0])
	c.Assert(err, check.IsNil)
	c.Assert(created.Address, check.Equals, "http://addr2:2")
	c.Assert(buf.String(), check.Matches, "(?s).*my destroy error.*")

	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr2:2")

	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr2")
}

func (s *S) TestHealerHandleError(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	healer.started = time.Now().Add(-3 * time.Second)
	conf := healerConfig()
	err = conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	nodes[0].(*provisiontest.FakeNode).SetHealth(2, true)

	waitTime := healer.HandleError(nodes[0].(provision.NodeHealthChecker))
	c.Assert(waitTime, check.Equals, time.Duration(0))

	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr2:2")

	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr2")
	c.Assert(eventtest.EventDesc{
		Target: event.Target{Type: "node", Value: "http://addr1:1"},
		Kind:   "healer",
		StartCustomData: map[string]interface{}{
			"reason":       "2 consecutive failures",
			"node.address": "http://addr1:1",
		},
		EndCustomData: map[string]interface{}{
			"address": "http://addr2:2",
		},
	}, eventtest.HasEvent)
}

func (s *S) TestHealerHandleErrorFailureEvent(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	healer.started = time.Now().Add(-3 * time.Second)
	conf := healerConfig()
	err = conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	p.PrepareFailure("AddNode", fmt.Errorf("error registering new node"))
	nodes[0].(*provisiontest.FakeNode).SetHealth(2, true)

	waitTime := healer.HandleError(nodes[0].(provision.NodeHealthChecker))
	c.Assert(waitTime, check.Equals, time.Duration(0))

	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	c.Assert(eventtest.EventDesc{
		Target: event.Target{Type: "node", Value: "http://addr1:1"},
		Kind:   "healer",
		StartCustomData: map[string]interface{}{
			"reason":       "2 consecutive failures",
			"node.address": "http://addr1:1",
		},
		ErrorMatches: `Can't auto-heal after 2 failures for node addr1: error registering new node: error registering new node`,
	}, eventtest.HasEvent)
}

func (s *S) TestHealerHandleErrorDoesntTriggerEventIfNotNeeded(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	err := p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		DisabledTime:          20,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	node, err := p.GetNode("http://addr1:1")
	c.Assert(err, check.IsNil)
	node.(*provisiontest.FakeNode).SetHealth(2, true)
	waitTime := healer.HandleError(node.(provision.NodeHealthChecker))
	c.Assert(waitTime, check.Equals, time.Duration(20))
	c.Assert(eventtest.EventDesc{
		IsEmpty: true,
	}, eventtest.HasEvent)
	node.(*provisiontest.FakeNode).SetHealth(0, true)
	err = p.UpdateNode(provision.UpdateNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "invalid"},
	})
	c.Assert(err, check.IsNil)
	node, err = p.GetNode("http://addr1:1")
	c.Assert(err, check.IsNil)
	waitTime = healer.HandleError(node.(provision.NodeHealthChecker))
	c.Assert(waitTime, check.Equals, time.Duration(20))
	c.Assert(eventtest.EventDesc{
		IsEmpty: true,
	}, eventtest.HasEvent)
	node.(*provisiontest.FakeNode).SetHealth(2, true)
	waitTime = healer.HandleError(node.(provision.NodeHealthChecker))
	c.Assert(waitTime, check.Equals, time.Duration(20))
	c.Assert(eventtest.EventDesc{
		IsEmpty: true,
	}, eventtest.HasEvent)
}

func (s *S) TestHealerHandleErrorThrottled(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.Shutdown()
	healer.started = time.Now().Add(-3 * time.Second)
	conf := healerConfig()
	err = conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")
	for i := 0; i < 3; i++ {
		var evt *event.Event
		evt, err = event.NewInternal(&event.Opts{
			Target:       event.Target{Type: "node", Value: nodes[0].Address()},
			InternalKind: "healer",
			Allowed:      event.Allowed(permission.PermPoolReadEvents),
		})
		c.Assert(err, check.IsNil)
		err = evt.Done(nil)
		c.Assert(err, check.IsNil)
	}
	err = healer.tryHealingNode(nodes[0], "myreason", nil)
	c.Assert(err, check.ErrorMatches, "Error trying to insert node healing event, healing aborted: event throttled, limit for healer on node \".*?\" is 3 every 5m0s")
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
}

func (s *S) TestHealerUpdateNodeData(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	data := provision.NodeStatusData{
		Addrs: []string{"addr1"},
		Checks: []provision.NodeCheckResult{
			{Name: "ok1", Successful: true},
			{Name: "ok2", Successful: true},
		},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	coll, err := nodeDataCollection()
	c.Assert(err, check.IsNil)
	defer coll.Close()
	var result nodeStatusData
	err = coll.FindId(nodeAddr).One(&result)
	c.Assert(err, check.IsNil)
	c.Assert(result.LastSuccess.IsZero(), check.Equals, false)
	c.Assert(result.LastUpdate.IsZero(), check.Equals, false)
	c.Assert(result.Checks[0].Time.IsZero(), check.Equals, false)
	result.LastUpdate = time.Time{}
	result.LastSuccess = time.Time{}
	result.Checks[0].Time = time.Time{}
	c.Assert(result, check.DeepEquals, nodeStatusData{
		Address: nodeAddr,
		Checks:  []nodeChecks{{Checks: data.Checks}},
	})
}

func (s *S) TestHealerUpdateNodeDataSavesLast10Checks(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	for i := 0; i < 20; i++ {
		data := provision.NodeStatusData{
			Addrs: []string{"addr1"},
			Checks: []provision.NodeCheckResult{
				{Name: fmt.Sprintf("ok1-%d", i), Successful: true},
				{Name: fmt.Sprintf("ok2-%d", i), Successful: true},
			},
		}
		err = healer.UpdateNodeData(data)
		c.Assert(err, check.IsNil)
	}
	coll, err := nodeDataCollection()
	c.Assert(err, check.IsNil)
	defer coll.Close()
	var result nodeStatusData
	err = coll.FindId(nodeAddr).One(&result)
	c.Assert(err, check.IsNil)
	c.Assert(result.LastSuccess.IsZero(), check.Equals, false)
	c.Assert(result.LastUpdate.IsZero(), check.Equals, false)
	result.LastUpdate = time.Time{}
	result.LastSuccess = time.Time{}
	c.Assert(result.Checks, check.HasLen, 10)
	expectedChecks := []nodeChecks{}
	for i, check := range result.Checks {
		expectedChecks = append(expectedChecks, nodeChecks{
			Time: check.Time,
			Checks: []provision.NodeCheckResult{
				{Name: fmt.Sprintf("ok1-%d", 10+i), Successful: true},
				{Name: fmt.Sprintf("ok2-%d", 10+i), Successful: true},
			},
		})
	}
	c.Assert(result, check.DeepEquals, nodeStatusData{
		Address: nodeAddr,
		Checks:  expectedChecks,
	})
}

func (s *S) TestHealerUpdateNodeDataNodeAddrNotFound(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	data := provision.NodeStatusData{
		Addrs: []string{"10.0.0.1"},
		Checks: []provision.NodeCheckResult{
			{Name: "ok1", Successful: true},
			{Name: "ok2", Successful: true},
		},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.ErrorMatches, `\[node healer update\] node not found for addrs: \[10.0.0.1\]`)
}

func (s *S) TestHealerUpdateNodeDataNodeFromUnits(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	app := provisiontest.NewFakeApp("myapp", "python", 0)
	err = p.Provision(app)
	c.Assert(err, check.IsNil)
	_, err = p.AddUnits(app, 1, "web", nil)
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	units, err := p.Units(app)
	c.Assert(err, check.IsNil)
	c.Assert(units, check.HasLen, 1)
	data := provision.NodeStatusData{
		Units: []provision.UnitStatusData{
			{ID: units[0].ID},
		},
		Addrs: []string{"10.0.0.1"},
		Checks: []provision.NodeCheckResult{
			{Name: "ok1", Successful: true},
			{Name: "ok2", Successful: true},
		},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	coll, err := nodeDataCollection()
	c.Assert(err, check.IsNil)
	defer coll.Close()
	var result nodeStatusData
	err = coll.FindId(nodeAddr).One(&result)
	c.Assert(err, check.IsNil)
	c.Assert(result.LastSuccess.IsZero(), check.Equals, false)
	c.Assert(result.LastUpdate.IsZero(), check.Equals, false)
	c.Assert(result.Checks[0].Time.IsZero(), check.Equals, false)
	result.LastUpdate = time.Time{}
	result.LastSuccess = time.Time{}
	result.Checks[0].Time = time.Time{}
	c.Assert(result, check.DeepEquals, nodeStatusData{
		Address: nodeAddr,
		Checks:  []nodeChecks{{Checks: data.Checks}},
	})
}

func (s *S) TestHealerUpdateNodeDataAmbiguousAddrs(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr1 := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr1,
	})
	c.Assert(err, check.IsNil)
	nodeAddr2 := "http://addr2:2"
	err = p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr2,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	data := provision.NodeStatusData{
		Addrs: []string{"addr1", "addr2"},
		Checks: []provision.NodeCheckResult{
			{Name: "ok1", Successful: true},
			{Name: "ok2", Successful: true},
		},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.ErrorMatches, `\[node healer update\] addrs match multiple nodes: \[.*? .*?\]`)
}

func (s *S) TestFindNodesForHealingNoNodes(c *check.C) {
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err := p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	nodes, nodesMap, err := healer.findNodesForHealing()
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 0)
	n, err := p.GetNode(nodeAddr)
	c.Assert(err, check.IsNil)
	c.Assert(nodesMap, check.DeepEquals, map[string]provision.Node{
		n.Address(): n,
	})
}

func (s *S) TestFindNodesForHealingWithConfNoEntries(c *check.C) {
	conf := healerConfig()
	err := conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err = p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	time.Sleep(1200 * time.Millisecond)
	nodes, nodesMap, err := healer.findNodesForHealing()
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 0)
	n, err := p.GetNode(nodeAddr)
	c.Assert(err, check.IsNil)
	c.Assert(nodesMap, check.DeepEquals, map[string]provision.Node{
		n.Address(): n,
	})
}

func (s *S) TestFindNodesForHealingLastUpdateDefault(c *check.C) {
	conf := healerConfig()
	err := conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err = p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	healer.started = time.Now().Add(-3 * time.Second)
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, nodesMap, err := healer.findNodesForHealing()
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	n, err := p.GetNode(nodeAddr)
	c.Assert(err, check.IsNil)
	c.Assert(nodesMap, check.DeepEquals, map[string]provision.Node{
		n.Address(): n,
	})
}

func (s *S) TestFindNodesForHealingLastUpdateWithRecentStarted(c *check.C) {
	conf := healerConfig()
	err := conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	p := provisiontest.NewFakeProvisioner()
	nodeAddr := "http://addr1:1"
	err = p.AddNode(provision.AddNodeOptions{
		Address: nodeAddr,
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner: p,
	})
	healer.Shutdown()
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, nodesMap, err := healer.findNodesForHealing()
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 0)
	n, err := p.GetNode(nodeAddr)
	c.Assert(err, check.IsNil)
	c.Assert(nodesMap, check.DeepEquals, map[string]provision.Node{
		n.Address(): n,
	})
}

func (s *S) TestCheckActiveHealing(c *check.C) {
	conf := healerConfig()
	err := conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err = iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")

	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)

	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:        p,
		WaitTimeNewMachine: time.Minute,
	})
	healer.Shutdown()
	healer.started = time.Now().Add(-3 * time.Second)

	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)

	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")

	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")

	healer.runActiveHealing()

	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr2:2")

	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr2")

	c.Assert(eventtest.EventDesc{
		Target: event.Target{Type: "node", Value: "http://addr1:1"},
		Kind:   "healer",
		StartCustomData: map[string]interface{}{
			"reason":         bson.M{"$regex": `last update \d+\.\d*?s ago, last success \d+\.\d*?s ago`},
			"lastcheck.time": bson.M{"$exists": true},
			"node.address":   "http://addr1:1",
		},
		EndCustomData: map[string]interface{}{
			"address": "http://addr2:2",
		},
	}, eventtest.HasEvent)
}

func (s *S) TestTryHealingNodeConcurrent(c *check.C) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(10))
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.started = time.Now().Add(-3 * time.Second)
	conf := healerConfig()
	err = conf.SaveBase(NodeHealerConfig{Enabled: boolPtr(true), MaxUnresponsiveTime: intPtr(1)})
	c.Assert(err, check.IsNil)
	data := provision.NodeStatusData{
		Addrs:  []string{"addr1"},
		Checks: []provision.NodeCheckResult{},
	}
	err = healer.UpdateNodeData(data)
	c.Assert(err, check.IsNil)
	time.Sleep(1200 * time.Millisecond)
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")
	wg := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			healErr := healer.tryHealingNode(nodes[0], "something", nil)
			c.Assert(healErr, check.IsNil)
		}()
	}
	wg.Wait()
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr2:2")
	machines, err = iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr2")
	c.Assert(eventtest.EventDesc{
		Target: event.Target{Type: "node", Value: "http://addr1:1"},
		Kind:   "healer",
		StartCustomData: map[string]interface{}{
			"reason":       "something",
			"node.address": "http://addr1:1",
		},
		EndCustomData: map[string]interface{}{
			"address": "http://addr2:2",
		},
	}, eventtest.HasEvent)
}

func (s *S) TestTryHealingNodeDoubleCheck(c *check.C) {
	factory, iaasInst := iaasTesting.NewHealerIaaSConstructorWithInst("addr1")
	iaas.RegisterIaasProvider("my-healer-iaas", factory)
	_, err := iaas.CreateMachineForIaaS("my-healer-iaas", map[string]string{})
	c.Assert(err, check.IsNil)
	iaasInst.Addr = "addr2"
	config.Set("iaas:node-protocol", "http")
	config.Set("iaas:node-port", 2)
	defer config.Unset("iaas:node-protocol")
	defer config.Unset("iaas:node-port")
	p := provisiontest.NewFakeProvisioner()
	err = p.AddNode(provision.AddNodeOptions{
		Address:  "http://addr1:1",
		Metadata: map[string]string{"iaas": "my-healer-iaas"},
	})
	c.Assert(err, check.IsNil)
	healer := NewNodeHealer(NodeHealerArgs{
		Provisioner:           p,
		FailuresBeforeHealing: 1,
		WaitTimeNewMachine:    time.Minute,
	})
	healer.started = time.Now().Add(-3 * time.Second)
	nodes, err := p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	healErr := healer.tryHealingNode(nodes[0], "something", nil)
	c.Assert(healErr, check.IsNil)
	nodes, err = p.ListNodes(nil)
	c.Assert(err, check.IsNil)
	c.Assert(nodes, check.HasLen, 1)
	c.Assert(nodes[0].Address(), check.Equals, "http://addr1:1")
	machines, err := iaas.ListMachines()
	c.Assert(err, check.IsNil)
	c.Assert(machines, check.HasLen, 1)
	c.Assert(machines[0].Address, check.Equals, "addr1")
	c.Assert(eventtest.EventDesc{
		IsEmpty: true,
	}, eventtest.HasEvent)
}

func (s *S) TestUpdateConfigIgnoresEmpty(c *check.C) {
	err := UpdateConfig("", NodeHealerConfig{
		Enabled:             boolPtr(true),
		MaxUnresponsiveTime: intPtr(1),
	})
	c.Assert(err, check.IsNil)
	conf := healerConfig()
	var nodeConf NodeHealerConfig
	err = conf.Load("p1", &nodeConf)
	c.Assert(err, check.IsNil)
	c.Assert(nodeConf, check.DeepEquals, NodeHealerConfig{
		Enabled:                      boolPtr(true),
		MaxUnresponsiveTime:          intPtr(1),
		EnabledInherited:             true,
		MaxUnresponsiveTimeInherited: true,
		MaxTimeSinceSuccessInherited: true,
	})
	err = UpdateConfig("p1", NodeHealerConfig{
		MaxTimeSinceSuccess: intPtr(2),
	})
	c.Assert(err, check.IsNil)
	nodeConf = NodeHealerConfig{}
	err = conf.Load("p1", &nodeConf)
	c.Assert(err, check.IsNil)
	c.Assert(nodeConf, check.DeepEquals, NodeHealerConfig{
		Enabled:                      boolPtr(true),
		MaxUnresponsiveTime:          intPtr(1),
		MaxTimeSinceSuccess:          intPtr(2),
		EnabledInherited:             true,
		MaxUnresponsiveTimeInherited: true,
		MaxTimeSinceSuccessInherited: false,
	})
	err = UpdateConfig("p1", NodeHealerConfig{
		MaxTimeSinceSuccess: intPtr(2),
		MaxUnresponsiveTime: intPtr(9),
	})
	c.Assert(err, check.IsNil)
	nodeConf = NodeHealerConfig{}
	err = conf.Load("p1", &nodeConf)
	c.Assert(err, check.IsNil)
	c.Assert(nodeConf, check.DeepEquals, NodeHealerConfig{
		Enabled:                      boolPtr(true),
		MaxUnresponsiveTime:          intPtr(9),
		MaxTimeSinceSuccess:          intPtr(2),
		EnabledInherited:             true,
		MaxUnresponsiveTimeInherited: false,
		MaxTimeSinceSuccessInherited: false,
	})
}

func boolPtr(b bool) *bool {
	return &b
}

func intPtr(i int) *int {
	return &i
}