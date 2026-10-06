// Package ipmi 实现了纯标准库的 IPMI 2.0（RMCP+）客户端，用于「手动资源」
// 插件对独立服务器的电源操作（对齐魔方 CBAP ManualResource 插件的 IPMI 能力）。
//
// 协商算法固定请求 RAKP-HMAC-SHA1 + HMAC-SHA1-96 完整性 + AES-CBC-128 机密性
// （即最常用的 cipher suite 3 语义），字段与推导按 ipmitool 的 lanplus 实现逐项对齐：
//
//	SIK = HMAC-SHA1(Kuid, Rm | Rc | ROLE | ULEN | USERNAME)   // Rm=console 随机, Rc=BMC 随机
//	K1  = HMAC-SHA1(SIK, 0x01 * 20)                           // 本端发包的 authcode 密钥
//	K2  = HMAC-SHA1(SIK, 0x02 * 20)                           // 对端发包 authcode 密钥 + AES 密钥（前 16 字节）
//	RAKP2/3 authcode = HMAC-SHA1(Kuid, Rc | SIDm | ROLE | ULEN | USERNAME)
//	RAKP4 authcode   = HMAC-SHA1(SIK, Rm | SIDc | GUIDc)（取前 12 位校验）
//
// 帧格式（多字节一律小端）：RMCP 头 4 字节（06 00 FF 07）+ AuthType 0x06 +
// PayloadType（0x40 完整性 / 0x80 机密性）+ 会话 ID + 序号 + 载荷长度 + 载荷 +
// 完整性填充（0xFF）+ 填充长度 + Next Header(0x07) + authcode（SHA1-96 取 12 字节）。
// 完整性覆盖 AuthType..Next Header；机密性载荷 = IV(16) || AES-CBC-128(K2[:16], 载荷+填充+填充长度)。
package ipmi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// Chassis 控制码（IPMI 2.0 表 28-3）。
const (
	ChassisPowerOn    = 0x01
	ChassisPowerOff   = 0x00
	ChassisPowerCycle = 0x02
)

// RMCP+ / IPMI 常量。
const (
	rakpAuthSHA1      = 0x01
	integritySHA1_96  = 0x01
	confidentialAES   = 0x01
	sha1AuthcodeLen   = 12 // SHA1-96
	networkFunction   = 0x06
	chassisFunction   = 0x00
	cmdChassisStatus  = 0x01
	cmdChassisControl = 0x04
	remoteSWID        = 0x20 // BMC SWID
	consoleSWID       = 0x81 // 本端 SWID（软件）
)

// 错误集合：调用方按错误文案提示，均不回退。
var (
	ErrAuthFailed  = errors.New("ipmi: authentication failed (wrong password or user)")
	ErrProtocol    = errors.New("ipmi: protocol violation")
	ErrBMCError    = errors.New("ipmi: BMC returned an error")
	ErrIntegrity   = errors.New("ipmi: packet integrity check failed")
	ErrUnsupported = errors.New("ipmi: BMC negotiated unsupported algorithms")
)

// Client 是一个 IPMI 2.0 客户端。每次操作建立一次会话（电源操作低频，无需维持长连接）。
type Client struct {
	Addr     string // BMC 地址 host:port（默认端口 623）
	Username string
	Password string
	Timeout  time.Duration // 单次收包超时，默认 3s
}

// ChassisStatus 是机箱电源状态。
type ChassisStatus struct {
	PowerOn bool
}

// Status 读取电源状态（Chassis Status，NetFn 0x00 / Cmd 0x01）。
func (c *Client) Status(ctx context.Context) (ChassisStatus, error) {
	out := ChassisStatus{}
	resp, err := c.command(ctx, chassisFunction, cmdChassisStatus, nil)
	if err != nil {
		return out, err
	}
	if len(resp) < 1 {
		return out, ErrProtocol
	}
	out.PowerOn = resp[0]&0x01 != 0
	return out, nil
}

// PowerOn 开机。
func (c *Client) PowerOn(ctx context.Context) error {
	return c.powerControl(ctx, ChassisPowerOn)
}

// PowerOff 关机（软关机）。
func (c *Client) PowerOff(ctx context.Context) error {
	return c.powerControl(ctx, ChassisPowerOff)
}

// PowerCycle 断电再上电（等效重启）。
func (c *Client) PowerCycle(ctx context.Context) error {
	return c.powerControl(ctx, ChassisPowerCycle)
}

func (c *Client) powerControl(ctx context.Context, control byte) error {
	_, err := c.command(ctx, chassisFunction, cmdChassisControl, []byte{control})
	return err
}

// command 建立一次 RMCP+ 会话并执行一条 IPMI 命令，返回响应数据（不含完成码）。
func (c *Client) command(ctx context.Context, netfn, cmd byte, data []byte) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", c.Addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(timeout * 2)
	}
	_ = conn.SetDeadline(deadline)

	s, err := c.openSession(conn)
	if err != nil {
		return nil, err
	}
	if err := s.rakpExchange(conn); err != nil {
		return nil, err
	}
	return s.sendCommand(conn, netfn, cmd, data)
}

// ---- 会话建立 ----

type session struct {
	c           *Client
	consoleSID  uint32
	bmcSID      uint32
	consoleRand [16]byte
	bmcRand     [16]byte
	bmcGUID     [16]byte
	role        byte // 0x14: administrator(0x04) | lookup bit(0x10)
	sik         []byte
	k1          []byte
	k2          []byte
	outSeq      uint32
}

func (c *Client) openSession(conn net.Conn) (*session, error) {
	s := &session{c: c, consoleSID: randUint32(), outSeq: 1}
	if _, err := rand.Read(s.consoleRand[:]); err != nil {
		return nil, err
	}
	s.role = 0x14 // administrator + lookup bit（ipmitool 默认 0x04 | 0x10）

	payload := make([]byte, 32)
	payload[0] = 0 // tag
	payload[1] = 0x04
	binary.LittleEndian.PutUint32(payload[4:], s.consoleSID)
	payload[8], payload[11] = 0, 8
	payload[12] = rakpAuthSHA1
	payload[16], payload[19] = 1, 8
	payload[20] = integritySHA1_96
	payload[24], payload[27] = 2, 8
	payload[28] = confidentialAES

	resp, err := s.roundTrip(conn, 0x10, false, payload)
	if err != nil {
		return nil, err
	}
	if len(resp) < 36 {
		return nil, ErrProtocol
	}
	if resp[1] != 0 {
		return nil, fmt.Errorf("%w: open session code %d", ErrBMCError, resp[1])
	}
	s.bmcSID = binary.LittleEndian.Uint32(resp[8:])
	// BMC 逐项回填协商结果；只接受请求的算法组合，避免静默降级。
	if resp[16] != rakpAuthSHA1 || resp[24] != integritySHA1_96 || resp[32] != confidentialAES {
		return nil, ErrUnsupported
	}
	return s, nil
}

func (s *session) rakpExchange(conn net.Conn) error {
	user := []byte(s.c.Username)
	msg1 := make([]byte, 28+len(user))
	binary.LittleEndian.PutUint32(msg1[4:], s.bmcSID)
	copy(msg1[8:], s.consoleRand[:])
	msg1[24] = s.role
	msg1[27] = byte(len(user))
	copy(msg1[28:], user)

	resp, err := s.roundTrip(conn, 0x12, false, msg1)
	if err != nil {
		return err
	}
	if len(resp) < 60 {
		return ErrProtocol
	}
	if resp[1] != 0 {
		if resp[1] == 0x0d || resp[1] == 0x0f || resp[1] == 0x10 {
			return ErrAuthFailed
		}
		return fmt.Errorf("%w: RAKP2 code %d", ErrBMCError, resp[1])
	}
	copy(s.bmcRand[:], resp[8:])
	copy(s.bmcGUID[:], resp[24:])
	sik, err := s.deriveKeys(user)
	if err != nil {
		return err
	}
	s.sik = sik
	s.k1 = hmacSHA1(s.sik, []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1})
	s.k2 = hmacSHA1(s.sik, []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2})

	// RAKP2 的 authcode 校验：HMAC-SHA1(Kuid, Rc | SIDm | ROLE | ULEN | USERNAME)。
	macInput := make([]byte, 0, 16+4+1+1+len(user))
	macInput = append(macInput, s.bmcRand[:]...)
	macInput = binary.LittleEndian.AppendUint32(macInput, s.consoleSID)
	macInput = append(macInput, s.role, byte(len(user)))
	macInput = append(macInput, user...)
	expect := hmacSHA1([]byte(s.c.Password), macInput)
	if !hmac.Equal(expect[:12], resp[40:52]) {
		return ErrAuthFailed
	}

	// RAKP3：发送同样的 authcode，完成密钥交换。
	msg3 := make([]byte, 8+len(expect))
	binary.LittleEndian.PutUint32(msg3[4:], s.bmcSID)
	copy(msg3[8:], expect[:])
	rakp4, err := s.roundTrip(conn, 0x14, false, msg3)
	if err != nil {
		return err
	}
	// RAKP4 的完整性校验值 = HMAC-SHA1(SIK, Rm | SIDc | GUIDc)，取前 12 位比对。
	if len(rakp4) < 20 {
		return ErrProtocol
	}
	if rakp4[1] != 0 {
		return fmt.Errorf("%w: RAKP4 code %d", ErrBMCError, rakp4[1])
	}
	mac4 := make([]byte, 0, 16+4+16)
	mac4 = append(mac4, s.consoleRand[:]...)
	mac4 = binary.LittleEndian.AppendUint32(mac4, s.bmcSID)
	mac4 = append(mac4, s.bmcGUID[:]...)
	if !hmac.Equal(hmacSHA1(s.sik, mac4)[:12], rakp4[8:20]) {
		return ErrIntegrity
	}
	return nil
}

// deriveKeys 由用户密码推导 SIK；推导输入与 ipmitool lanplus_generate_sik 一致。
func (s *session) deriveKeys(user []byte) ([]byte, error) {
	in := make([]byte, 0, 16+16+1+1+len(user))
	in = append(in, s.consoleRand[:]...) // Rm
	in = append(in, s.bmcRand[:]...)     // Rc
	in = append(in, s.role, byte(len(user)))
	in = append(in, user...)
	return hmacSHA1([]byte(s.c.Password), in), nil
}

// ---- 活跃会话收发 ----

// sendCommand 发送一条 IPMI 命令并解出响应数据。
func (s *session) sendCommand(conn net.Conn, netfn, cmd byte, data []byte) ([]byte, error) {
	payload := make([]byte, 0, 5+len(data))
	payload = append(payload, remoteSWID, netfn<<2, (s.nextRqSeq())<<2, cmd)
	payload = append(payload, data...)

	resp, err := s.roundTrip(conn, 0x00, true, payload)
	if err != nil {
		return nil, err
	}
	// 响应载荷：rqAddr(0x81) | (netfn|1)<<2 | rqSeq | cmd | cc | data...
	if len(resp) < 6 || resp[0] != consoleSWID || resp[1] != (netfn|1)<<2 || resp[3] != cmd {
		return nil, ErrProtocol
	}
	if resp[4] != 0 {
		return nil, fmt.Errorf("%w: completion code 0x%02x", ErrBMCError, resp[4])
	}
	return resp[5:], nil
}

func (s *session) nextRqSeq() byte {
	s.outSeq++
	return byte(s.outSeq & 0x3f)
}

// roundTrip 组帧发送一个载荷并收取（可选解密）响应载荷。
// RAKP 载荷（0x10/0x12/0x14）的响应类型是 +1；IPMI 命令（0x00）的响应同为 0x00，
// 由响应载荷自身的「netfn|1」区分方向。
func (s *session) roundTrip(conn net.Conn, payloadType byte, encrypted bool, payload []byte) ([]byte, error) {
	frame, _, err := s.buildFrame(payloadType, encrypted, payload)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(frame); err != nil {
		return nil, err
	}
	want := payloadType + 1
	if payloadType == 0x00 {
		want = 0x00
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		respType, body, ok, err := s.parseFrame(buf[:n])
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // 非本次会话的杂散包
		}
		if respType == want {
			return body, nil
		}
	}
}

// buildFrame 组一个 RMCP+ 帧；返回完整帧与 authcode（未激活会话时为空）。
func (s *session) buildFrame(payloadType byte, encrypted bool, payload []byte) ([]byte, []byte, error) {
	active := s.bmcSID != 0
	wire := payload
	authcode := []byte{}
	flags := byte(0)
	if encrypted && active {
		flags |= 0x80
		wire = s.encrypt(payload)
	}
	head := make([]byte, 16)
	head[0], head[1], head[2], head[3] = 0x06, 0x00, 0xff, 0x07
	head[4] = 0x06 // IPMI v2 format
	head[5] = payloadType | flags
	if active {
		binary.LittleEndian.PutUint32(head[6:], s.bmcSID)
		binary.LittleEndian.PutUint32(head[10:], s.outSeq)
		s.outSeq++
	}
	binary.LittleEndian.PutUint16(head[14:], uint16(len(wire)))
	out := append(head, wire...)

	if active {
		// 完整性：AuthType..Next Header（4 字节对齐）之后追 authcode。
		// 对齐范围 = 12(头部) + 载荷 + 填充 + 2(填充长度 + Next Header)。
		pad := (4 - (len(out)-2)%4) % 4
		for i := 0; i < pad; i++ {
			out = append(out, 0xff)
		}
		out = append(out, byte(pad), 0x07)
		flags |= 0x40
		out[5] = payloadType | flags
		mac := hmacSHA1(s.k1, out[4:])
		authcode = mac[:sha1AuthcodeLen]
		out = append(out, authcode...)
	}
	return out, authcode, nil
}

// parseFrame 解一帧：返回载荷类型与（已验签解密的）载荷体。
func (s *session) parseFrame(pkt []byte) (byte, []byte, bool, error) {
	if len(pkt) < 16 || pkt[0] != 0x06 || pkt[3] != 0x07 {
		return 0, nil, false, nil // RMCP 杂散包
	}
	if pkt[4] != 0x06 {
		return 0, nil, false, nil
	}
	payloadType := pkt[5] &^ 0xc0
	encrypted := pkt[5]&0x80 != 0
	authenticated := pkt[5]&0x40 != 0
	plen := int(binary.LittleEndian.Uint16(pkt[14:]))
	if 16+plen > len(pkt) {
		return 0, nil, false, ErrProtocol
	}
	body := pkt[16 : 16+plen]
	tail := pkt[16+plen:]
	if authenticated {
		// tail = pad(0xFF×padLen) + padLen + nextHeader(0x07) + authcode(12)
		if len(tail) < sha1AuthcodeLen+2 {
			return 0, nil, false, ErrProtocol
		}
		padLen := int(tail[len(tail)-sha1AuthcodeLen-2])
		if len(tail) < sha1AuthcodeLen+2+padLen {
			return 0, nil, false, ErrProtocol
		}
		// AuthType..Next Header 的长度必须已按 4 字节对齐。
		if (16+plen+padLen+2-4)%4 != 0 {
			return 0, nil, false, ErrProtocol
		}
		for i := 0; i < padLen; i++ {
			if tail[len(tail)-sha1AuthcodeLen-2-padLen+i] != 0xff {
				return 0, nil, false, ErrProtocol
			}
		}
		if tail[len(tail)-sha1AuthcodeLen-1] != 0x07 {
			return 0, nil, false, ErrProtocol
		}
		span := pkt[4 : 16+plen+padLen+2]
		mac := hmacSHA1(s.k2, span)[:sha1AuthcodeLen]
		if !hmac.Equal(mac, tail[len(tail)-sha1AuthcodeLen:]) {
			return 0, nil, false, ErrIntegrity
		}
	}
	if encrypted {
		dec, err := s.decrypt(body)
		if err != nil {
			return 0, nil, false, err
		}
		body = dec
	}
	return payloadType, body, true, nil
}

// encrypt 按机密性规则输出 IV || AES-CBC-128(K2[:16], 载荷+填充+填充长度)。
func (s *session) encrypt(payload []byte) []byte {
	mod := (len(payload) + 1) % 16
	pad := 0
	if mod != 0 {
		pad = 16 - mod
	}
	plain := make([]byte, 0, len(payload)+pad+1)
	plain = append(plain, payload...)
	for i := 0; i < pad; i++ {
		plain = append(plain, byte(i+1))
	}
	plain = append(plain, byte(pad))

	iv := make([]byte, 16)
	_, _ = rand.Read(iv)
	block, _ := aes.NewCipher(s.k2[:16])
	enc := cipher.NewCBCEncrypter(block, iv)
	out := make([]byte, len(plain))
	enc.CryptBlocks(out, plain)
	return append(iv, out...)
}

// decrypt 校验并解开 IV || 密文，返回原载荷。
func (s *session) decrypt(body []byte) ([]byte, error) {
	if len(body) < 32 || len(body)%16 != 0 {
		return nil, ErrProtocol
	}
	block, err := aes.NewCipher(s.k2[:16])
	if err != nil {
		return nil, err
	}
	dec := cipher.NewCBCDecrypter(block, body[:16])
	plain := make([]byte, len(body)-16)
	dec.CryptBlocks(plain, body[16:])
	padLen := int(plain[len(plain)-1])
	if padLen > 15 || len(plain)-1-padLen < 0 {
		return nil, ErrIntegrity
	}
	for i := 0; i < padLen; i++ {
		if plain[len(plain)-1-padLen+i] != byte(i+1) {
			return nil, ErrIntegrity
		}
	}
	return plain[:len(plain)-1-padLen], nil
}

func hmacSHA1(key, data []byte) []byte {
	m := hmac.New(sha1.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.LittleEndian.Uint32(b[:])
}
