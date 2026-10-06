package ipmi

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"testing"
)

// 推导向量独立用 Python hashlib 复算钉死（Kuid="password"，user="admin"，
// Rm=00..0f、Rc=10..1f、GUID=20..2f、SIDm=0xA0A2A3A4、SIDc=0x11223344、ROLE=0x14）。
// 任何一处输入顺序或编码写错都会表现为「验签失败」，所以用固定向量锁死。

var vecSession = &session{
	consoleSID:  0xA0A2A3A4,
	bmcSID:      0x11223344,
	role:        0x14,
	consoleRand: [16]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
	bmcRand:     [16]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f},
	bmcGUID:     [16]byte{0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f},
	c:           &Client{Username: "admin", Password: "password"},
}

// mustHex 把十六进制串转成字节（固定向量使用）。
func mustHex(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		out[i] = hexVal(s[i*2])<<4 | hexVal(s[i*2+1])
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func TestDeriveSIKVector(t *testing.T) {
	sik, err := vecSession.deriveKeys([]byte("admin"))
	if err != nil {
		t.Fatal(err)
	}
	want := mustHex("122c77c4b11ccd93251cbae6c34a9cb6310da154")
	if !bytes.Equal(sik, want) {
		t.Fatalf("SIK = %x, want %x", sik, want)
	}
}

func TestDeriveK1K2Vectors(t *testing.T) {
	sik, _ := vecSession.deriveKeys([]byte("admin"))
	k1 := hmacSHA1(sik, bytes.Repeat([]byte{1}, 20))
	k2 := hmacSHA1(sik, bytes.Repeat([]byte{2}, 20))
	if want := mustHex("e4472be78f9a81fa68297aab696a7be8c97fc9f8"); !bytes.Equal(k1, want) {
		t.Fatalf("K1 = %x, want %x", k1, want)
	}
	if want := mustHex("2b6552012a2517cb3b5713901d757a6efc7d8301"); !bytes.Equal(k2, want) {
		t.Fatalf("K2 = %x, want %x", k2, want)
	}
}

func TestRAKPAuthcodeVector(t *testing.T) {
	// RAKP2/3 authcode = HMAC(Kuid, Rc | SIDm(LE) | ROLE | ULEN | USERNAME)
	in := []byte{}
	in = append(in, vecSession.bmcRand[:]...)
	in = binary.LittleEndian.AppendUint32(in, vecSession.consoleSID)
	in = append(in, vecSession.role, byte(len("admin")))
	in = append(in, []byte("admin")...)
	got := hmacSHA1([]byte("password"), in)
	if want := mustHex("4ab420258775814f120682c8ab9275c29997effd"); !bytes.Equal(got, want) {
		t.Fatalf("RAKP authcode = %x, want %x", got, want)
	}
}

func TestRAKP4ICVVector(t *testing.T) {
	// RAKP4 ICV = HMAC(SIK, Rm | SIDc(LE) | GUIDc)[:12]
	in := []byte{}
	in = append(in, vecSession.consoleRand[:]...)
	in = binary.LittleEndian.AppendUint32(in, vecSession.bmcSID)
	in = append(in, vecSession.bmcGUID[:]...)
	sik, _ := vecSession.deriveKeys([]byte("admin"))
	got := hmacSHA1(sik, in)[:12]
	if want := mustHex("d9e15b7475416719001dc211"); !bytes.Equal(got, want) {
		t.Fatalf("RAKP4 ICV = %x, want %x", got, want)
	}
}

// ---- 加解密与帧完整性 ----

func testSession() *session {
	s := *vecSession
	s.sik = mustHex("122c77c4b11ccd93251cbae6c34a9cb6310da154")
	s.k1 = hmacSHA1(s.sik, bytes.Repeat([]byte{1}, 20))
	s.k2 = hmacSHA1(s.sik, bytes.Repeat([]byte{2}, 20))
	return &s
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	s := testSession()
	payload := []byte{0x20, 0x00, 0x08, 0x01, 0x01}
	for _, n := range []int{1, 4, 5, 16, 17, 31, 64} {
		p := bytes.Repeat(payload, n/len(payload)+1)[:n]
		sealed := s.encrypt(p)
		got, err := s.decrypt(sealed)
		if err != nil {
			t.Fatalf("len %d: %v", n, err)
		}
		if !bytes.Equal(got, p) {
			t.Fatalf("len %d: round trip mismatch", n)
		}
	}
}

func TestDecryptRejectsTamper(t *testing.T) {
	s := testSession()
	sealed := s.encrypt([]byte{1, 2, 3, 4, 5})
	sealed[len(sealed)-1] ^= 0xff // 篡改填充长度位
	if _, err := s.decrypt(sealed); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
}

func TestFrameIntegrityAndTamper(t *testing.T) {
	s := testSession()
	frame, _, err := s.buildFrame(0x00, false, []byte{0x20, 0x00, 0x04, 0x01, 0x01})
	if err != nil {
		t.Fatal(err)
	}
	// parseFrame 按「解析对端发来的包」用 k2 验签；本帧是 k1 签的，
	// 因此这里把 k2 换成 k1 模拟 BMC 端视角。
	bmcView := *s
	bmcView.k2 = s.k1
	_, body, ok, err := bmcView.parseFrame(frame)
	if err != nil || !ok {
		t.Fatalf("self parse: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(body, []byte{0x20, 0x00, 0x04, 0x01, 0x01}) {
		t.Fatalf("body mismatch: %x", body)
	}

	tampered := append([]byte(nil), frame...)
	tampered[16] ^= 0x01 // 翻转载荷第一个字节
	if _, _, _, err := bmcView.parseFrame(tampered); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
}

// ---- 假 BMC 全流程 ----
// 假 BMC 按同一套协议实现服务端（OpenSession → RAKP2 → RAKP4 → 会话命令），
// 用于端到端验证握手、密钥推导一致性、加解密与机箱命令；密码不一致时
// 客户端必须在 RAKP2 校验处得到 ErrAuthFailed。

type fakeBMC struct {
	conn    *net.UDPConn
	pass    string
	powerOn bool
	rand    [16]byte
	guid    [16]byte
	s       *bmcSess
}

type bmcSess struct {
	sid         uint32
	consoleSID  uint32
	rm          [16]byte
	role        byte
	user        string
	sik, k1, k2 []byte
	outSeq      uint32
}

func startFakeBMC(t *testing.T, pass string) *fakeBMC {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBMC{conn: conn, pass: pass}
	for i := range b.rand {
		b.rand[i] = byte(0x40 + i)
	}
	for i := range b.guid {
		b.guid[i] = byte(0x60 + i)
	}
	go b.serve()
	t.Cleanup(func() { conn.Close() })
	return b
}

func (b *fakeBMC) serve() {
	buf := make([]byte, 4096)
	for {
		n, addr, err := b.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		b.handle(append([]byte(nil), buf[:n]...), addr)
	}
}

func (b *fakeBMC) handle(pkt []byte, addr *net.UDPAddr) {
	switch pkt[5] &^ 0xc0 {
	case 0x10: // Open Session Request
		if b.s == nil {
			b.s = &bmcSess{sid: 0x11223344}
		}
		b.s.consoleSID = binary.LittleEndian.Uint32(pkt[20:])
		resp := make([]byte, 36)
		resp[0] = pkt[16] // tag
		resp[2] = 0x04    // max priv
		binary.LittleEndian.PutUint32(resp[4:], b.s.consoleSID)
		binary.LittleEndian.PutUint32(resp[8:], b.s.sid)
		resp[16], resp[24], resp[32] = rakpAuthSHA1, integritySHA1_96, confidentialAES
		b.reply(addr, 0x11, resp)
	case 0x12: // RAKP 1 → RAKP 2
		s := b.s
		if s == nil {
			return
		}
		copy(s.rm[:], pkt[24:40])
		s.role = pkt[40]
		ulen := int(pkt[43])
		s.user = string(pkt[44 : 44+ulen])
		in := append([]byte{}, s.rm[:]...) // Rm | Rc | ROLE | ULEN | USERNAME
		in = append(in, b.rand[:]...)
		in = append(in, s.role, byte(ulen))
		in = append(in, []byte(s.user)...)
		s.sik = hmacSHA1([]byte(b.pass), in)
		s.k1 = hmacSHA1(s.sik, bytes.Repeat([]byte{1}, 20))
		s.k2 = hmacSHA1(s.sik, bytes.Repeat([]byte{2}, 20))

		resp := make([]byte, 60)
		binary.LittleEndian.PutUint32(resp[4:], s.consoleSID)
		copy(resp[8:], b.rand[:])
		copy(resp[24:], b.guid[:])
		mac := append([]byte{}, b.rand[:]...) // Rc | SIDm | ROLE | ULEN | USERNAME
		mac = binary.LittleEndian.AppendUint32(mac, s.consoleSID)
		mac = append(mac, s.role, byte(ulen))
		mac = append(mac, []byte(s.user)...)
		copy(resp[40:], hmacSHA1([]byte(b.pass), mac))
		b.reply(addr, 0x13, resp)
	case 0x14: // RAKP 3 → RAKP 4
		s := b.s
		if s == nil {
			return
		}
		resp := make([]byte, 20)
		binary.LittleEndian.PutUint32(resp[4:], s.consoleSID)
		mac := append([]byte{}, s.rm[:]...) // Rm | SIDc | GUIDc
		mac = binary.LittleEndian.AppendUint32(mac, s.sid)
		mac = append(mac, b.guid[:]...)
		copy(resp[8:], hmacSHA1(s.sik, mac)[:12])
		b.reply(addr, 0x15, resp)
	default: // 会话内 IPMI 命令
		b.command(pkt, addr)
	}
}

func (b *fakeBMC) command(pkt []byte, addr *net.UDPAddr) {
	s := b.s
	if s == nil {
		return
	}
	plen := int(binary.LittleEndian.Uint16(pkt[14:]))
	body := pkt[16 : 16+plen]
	tail := pkt[16+plen:]
	if len(tail) < 14 {
		return
	}
	padLen := int(tail[len(tail)-14])
	if !bytes.Equal(hmacSHA1(s.k1, pkt[4:16+plen+padLen+2])[:12], tail[len(tail)-12:]) {
		return
	}
	plain, err := bmcDecrypt(s.k2[:16], body)
	if err != nil {
		return
	}
	netfn := plain[1] >> 2
	cmd := plain[3]
	var data byte
	switch {
	case netfn == chassisFunction && cmd == cmdChassisStatus:
		if b.powerOn {
			data = 0x01
		}
	case netfn == chassisFunction && cmd == cmdChassisControl:
		b.powerOn = plain[4] == ChassisPowerOn
	default:
		resp := append([]byte{consoleSWID, (netfn | 1) << 2, plain[2], cmd}, 0xc1)
		b.sendSession(addr, resp)
		return
	}
	resp := []byte{consoleSWID, (netfn | 1) << 2, plain[2], cmd, 0x00, data}
	b.sendSession(addr, resp)
}

func (b *fakeBMC) sendSession(addr *net.UDPAddr, payload []byte) {
	s := b.s
	sealed := bmcEncrypt(s.k2[:16], payload)
	out := make([]byte, 16+len(sealed))
	out[0], out[1], out[2], out[3] = 0x06, 0x00, 0xff, 0x07
	out[4] = 0x06
	out[5] = 0x00 | 0x80 | 0x40
	binary.LittleEndian.PutUint32(out[6:], s.sid)
	s.outSeq++
	binary.LittleEndian.PutUint32(out[10:], s.outSeq)
	binary.LittleEndian.PutUint16(out[14:], uint16(len(sealed)))
	copy(out[16:], sealed)
	pad := (4 - (len(out)-2)%4) % 4
	for i := 0; i < pad; i++ {
		out = append(out, 0xff)
	}
	out = append(out, byte(pad), 0x07)
	out = append(out, hmacSHA1(s.k2, out[4:])[:12]...)
	b.conn.WriteToUDP(out, addr)
}

func (b *fakeBMC) reply(addr *net.UDPAddr, ptype byte, payload []byte) {
	out := make([]byte, 16+len(payload))
	out[0], out[1], out[2], out[3] = 0x06, 0x00, 0xff, 0x07
	out[4] = 0x06
	out[5] = ptype
	binary.LittleEndian.PutUint16(out[14:], uint16(len(payload)))
	copy(out[16:], payload)
	b.conn.WriteToUDP(out, addr)
}

// bmcEncrypt/bmcDecrypt 是与客户端同规格的机密性变换（IV || AES-CBC-128）。
func bmcEncrypt(key []byte, payload []byte) []byte {
	mod := (len(payload) + 1) % 16
	pad := 0
	if mod != 0 {
		pad = 16 - mod
	}
	plain := append([]byte{}, payload...)
	for i := 0; i < pad; i++ {
		plain = append(plain, byte(i+1))
	}
	plain = append(plain, byte(pad))
	block, _ := aes.NewCipher(key)
	out := make([]byte, 16+len(plain))
	if _, err := rand.Read(out[:16]); err != nil {
		panic(err)
	}
	cipher.NewCBCEncrypter(block, out[:16]).CryptBlocks(out[16:], plain)
	return out
}

func bmcDecrypt(key, body []byte) ([]byte, error) {
	if len(body) < 32 || len(body)%16 != 0 {
		return nil, ErrProtocol
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(body)-16)
	cipher.NewCBCDecrypter(block, body[:16]).CryptBlocks(plain, body[16:])
	padLen := int(plain[len(plain)-1])
	if padLen > 15 {
		return nil, ErrIntegrity
	}
	return plain[:len(plain)-1-padLen], nil
}

// ---- 端到端 ----

func TestClientAgainstFakeBMC(t *testing.T) {
	b := startFakeBMC(t, "s3cret")
	c := &Client{Addr: b.conn.LocalAddr().String(), Username: "admin", Password: "s3cret", Timeout: 2 * 1e9}
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.PowerOn {
		t.Fatal("fresh BMC should be powered off")
	}
	if err := c.PowerOn(ctx); err != nil {
		t.Fatalf("power on: %v", err)
	}
	st, err = c.Status(ctx)
	if err != nil || !st.PowerOn {
		t.Fatalf("status after on: %+v err=%v", st, err)
	}
	if err := c.PowerCycle(ctx); err != nil {
		t.Fatalf("power cycle: %v", err)
	}
	if err := c.PowerOff(ctx); err != nil {
		t.Fatalf("power off: %v", err)
	}
	st, _ = c.Status(ctx)
	if st.PowerOn {
		t.Fatal("should be off")
	}
}

func TestClientWrongPassword(t *testing.T) {
	b := startFakeBMC(t, "s3cret")
	c := &Client{Addr: b.conn.LocalAddr().String(), Username: "admin", Password: "wrong", Timeout: 2 * 1e9}
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestClientRejectsIntegrityBreach(t *testing.T) {
	// 直接对 parseFrame 的路径已由 TestFrameIntegrityAndTamper 覆盖；
	// 这里补一个协议错误：响应载荷长度越界必须报 ErrProtocol。
	s := testSession()
	bad := make([]byte, 30)
	bad[0], bad[1], bad[2], bad[3] = 0x06, 0x00, 0xff, 0x07
	bad[4] = 0x06
	binary.LittleEndian.PutUint16(bad[14:], 9999)
	if _, _, _, err := s.parseFrame(bad); !errors.Is(err, ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
}
