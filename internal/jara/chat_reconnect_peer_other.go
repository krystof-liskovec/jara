//go:build !linux

package jara

import "net"

func chatPeerPID(net.Conn) int { return 0 }

func reconnectingChatRelay(int) bool { return false }

func reconnectingRelayAncestors() map[int]bool { return map[int]bool{} }
