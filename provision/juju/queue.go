// Copyright 2013 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package juju

import (
	"github.com/globocom/tsuru/log"
	"github.com/globocom/tsuru/provision"
	"github.com/globocom/tsuru/queue"
	"sort"
)

const (
	addUnitToLoadBalancer = "add-unit-to-lb"
	queueName             = "tsuru-provision-juju"
)

type qApp struct {
	name string
}

func (a *qApp) GetName() string {
	return a.name
}

func handle(msg *queue.Message) {
	switch msg.Action {
	case addUnitToLoadBalancer:
		if len(msg.Args) < 1 {
			log.Printf("Failed to handle %q: it requires at least one argument.", msg.Action)
			msg.Delete()
			return
		}
		a := qApp{name: msg.Args[0]}
		unitNames := msg.Args[1:]
		sort.Strings(unitNames)
		status, err := (&JujuProvisioner{}).collectStatus()
		if err != nil {
			log.Printf("Failed to handle %q: juju status failed.\n%s.", msg.Action, err)
			msg.Release(0)
			return
		}
		var units []provision.Unit
		for _, u := range status {
			if u.AppName != a.name {
				continue
			}
			n := sort.SearchStrings(unitNames, u.Name)
			if len(unitNames) == 0 ||
				n < len(unitNames) && unitNames[n] == u.Name {
				units = append(units, u)
			}
		}
		if len(units) == 0 {
			log.Printf("Failed to handle %q: units not found.", msg.Action)
			msg.Delete()
			return
		}
		var noId []string
		var ok []provision.Unit
		for _, u := range units {
			if u.InstanceId == "pending" || u.InstanceId == "" {
				noId = append(noId, u.Name)
			} else {
				ok = append(ok, u)
			}
		}
		if len(noId) == len(units) {
			msg.Release(0)
		} else {
			manager := ELBManager{}
			manager.Register(&a, ok...)
			msg.Delete()
			if len(noId) > 0 {
				args := []string{a.name}
				args = append(args, noId...)
				msg := queue.Message{
					Action: msg.Action,
					Args:   args,
				}
				msg.Put(queueName, 0)

			}
		}
	default:
		msg.Release(0)
	}
}

var handler = queue.Handler{F: handle, Queues: []string{queueName}}

func enqueue(msg *queue.Message) {
	msg.Put(queueName, 0)
	handler.Start()
}
