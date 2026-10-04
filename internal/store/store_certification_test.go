package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 实名信息的隐私约束必须逐条验证：绝不能落明文。

const certMasterKey = "test-master-key-for-certification"

// certStore 返回一个配好主密钥的 Store。
func certStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	s.MasterKey = []byte(certMasterKey)
	return s
}

func TestSubmitCertificationStoresNoPlaintext(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	const realName = "张三丰"
	const idNumber = "110101199003071234"

	cert, err := s.SubmitCertification(ctx, userID, CertificationInput{
		RealName: realName, IDNumber: idNumber, Provider: "manual", Approved: false,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if cert.Status != "pending" {
		t.Fatalf("status = %q, want pending", cert.Status)
	}
	if cert.RealNameMasked != "张*丰" {
		t.Fatalf("masked name = %q, want 张*丰", cert.RealNameMasked)
	}
	if strings.Contains(cert.IDNumberMasked, "19900307") {
		t.Fatalf("masked id %q leaks the birth date", cert.IDNumberMasked)
	}

	rows, err := s.DB.Query(ctx, `SELECT c.public_id::text,c.status,c.real_name_masked,c.id_number_masked,c.name_hash,c.id_number_hash FROM certifications c WHERE c.user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no certification row")
	}
	var pub, status, maskedName, maskedID, nameHash, idHash string
	if err := rows.Scan(&pub, &status, &maskedName, &maskedID, &nameHash, &idHash); err != nil {
		t.Fatal(err)
	}
	fields := []struct{ label, value string }{
		{"status", status}, {"real_name_masked", maskedName}, {"id_number_masked", maskedID},
		{"name_hash", nameHash}, {"id_number_hash", idHash},
	}
	for _, field := range fields {
		if strings.Contains(field.value, realName) {
			t.Fatalf("column %s contains the plaintext name: %q", field.label, field.value)
		}
		if strings.Contains(field.value, idNumber) {
			t.Fatalf("column %s contains the plaintext id number: %q", field.label, field.value)
		}
		if strings.Contains(field.value, "19900307") {
			t.Fatalf("column %s leaks the birth date: %q", field.label, field.value)
		}
	}
	if len(idHash) != 64 {
		t.Fatalf("id_number_hash length = %d, want 64 hex chars", len(idHash))
	}
	if want := CertHash([]byte(certMasterKey), idNumber); idHash != want {
		t.Fatalf("id_number_hash = %s, want the HMAC %s", idHash, want)
	}
	_ = pub
}

func TestCertificationHashIsSalted(t *testing.T) {
	// 同一个证件号在不同主密钥下必须得到不同指纹——否则一旦库被拖走，
	// 攻击者可以拿公开的身份证号字典直接反查。
	a := CertHash([]byte("key-a"), "110101199003071234")
	b := CertHash([]byte("key-b"), "110101199003071234")
	if a == b {
		t.Fatal("the same id number hashed to the same value under different master keys")
	}
}

func TestSubmitCertificationRequiresMasterKey(t *testing.T) {
	s := newTestStore(t)
	// 没配主密钥时必须明确报错，绝不能退化成存明文。
	_, err := s.SubmitCertification(context.Background(), seedUser(t, s, 0), CertificationInput{
		RealName: "张三", IDNumber: "110101199003071234",
	})
	if err == nil {
		t.Fatal("submitting without a master key must fail rather than store plaintext")
	}
	if !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Fatalf("error %q should mention the missing master key", err.Error())
	}
}

func TestCertificationApprovedFlow(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)

	if ok, err := s.IsCertified(ctx, userID); err != nil || ok {
		t.Fatalf("a fresh user must not be certified (ok=%v err=%v)", ok, err)
	}
	cert, err := s.SubmitCertification(ctx, userID, CertificationInput{
		RealName: "李四", IDNumber: "11010119900307123X", Provider: "aliyun_idcard",
		Approved: true, VerifiedBy: "aliyun_idcard", Gender: "male", BirthDate: "1990-03-07",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if cert.Status != "approved" {
		t.Fatalf("status = %q, want approved", cert.Status)
	}
	if cert.ReviewedAt == nil {
		t.Fatal("an approved certification must have a reviewed_at timestamp")
	}
	if ok, err := s.IsCertified(ctx, userID); err != nil || !ok {
		t.Fatalf("IsCertified after approval = %v/%v, want true", ok, err)
	}
	got, err := s.GetCertification(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Gender != "male" {
		t.Fatalf("gender = %q, want male", got.Gender)
	}
	if got.BirthDate == nil || got.BirthDate.Format("2006-01-02") != "1990-03-07" {
		t.Fatalf("birth date = %v, want 1990-03-07", got.BirthDate)
	}
}

func TestAdminReviewCertification(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	cert, err := s.SubmitCertification(ctx, userID, CertificationInput{
		RealName: "王五", IDNumber: "110101199003071234", Provider: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReviewCertification(ctx, cert.PublicID, false, "照片不清晰", 0); err != nil {
		t.Fatalf("reject: %v", err)
	}
	got, err := s.GetCertification(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
	if got.RejectReason != "照片不清晰" {
		t.Fatalf("reject reason = %q, want the admin text", got.RejectReason)
	}
	if ok, _ := s.IsCertified(ctx, userID); ok {
		t.Fatal("a rejected certification must not count as certified")
	}
	if err := s.ReviewCertification(ctx, cert.PublicID, true, "", 0); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got2, _ := s.GetCertification(ctx, userID)
	if got2.Status != "approved" {
		t.Fatalf("status = %q, want approved", got2.Status)
	}
	if got2.RejectReason != "" {
		t.Fatalf("reject reason = %q, want it cleared on approval", got2.RejectReason)
	}
	if err := s.ReviewCertification(ctx, "00000000-0000-0000-0000-000000000000", true, "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("review of a missing record = %v, want ErrNotFound", err)
	}
}

func TestResubmitOverwritesPrevious(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	if _, err := s.SubmitCertification(ctx, userID, CertificationInput{
		RealName: "赵六", IDNumber: "110101199003071234", Provider: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	// 填错了应该能改，而不是被一条错误记录锁死。
	cert, err := s.SubmitCertification(ctx, userID, CertificationInput{
		RealName: "赵六六", IDNumber: "11010119900307123X", Provider: "manual",
	})
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if cert.RealNameMasked != "赵*六" {
		t.Fatalf("masked name after resubmit = %q, want 赵*六", cert.RealNameMasked)
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM certifications WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("found %d certification rows, want exactly 1 after resubmitting", count)
	}
}

// 同一个证件号不能绑定两个已通过认证的账号——否则可以拿别人的证件开小号。
func TestSameIDNumberCannotCertifyTwoAccounts(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	const idNumber = "110101199003071234"
	u1 := seedUser(t, s, 0)
	u2 := seedUser(t, s, 0)
	if _, err := s.SubmitCertification(ctx, u1, CertificationInput{
		RealName: "甲", IDNumber: idNumber, Provider: "manual", Approved: true,
	}); err != nil {
		t.Fatalf("first account: %v", err)
	}
	_, err := s.SubmitCertification(ctx, u2, CertificationInput{
		RealName: "乙", IDNumber: idNumber, Provider: "manual", Approved: true,
	})
	if err == nil {
		t.Fatal("the same id number certified two accounts")
	}
	if !strings.Contains(err.Error(), "已被其它账号") {
		t.Fatalf("error %q should explain the duplicate binding", err.Error())
	}
	// 但 pending 状态不受唯一约束限制：还没通过审核的重复提交只是排队。
	if _, err := s.SubmitCertification(ctx, u2, CertificationInput{
		RealName: "乙", IDNumber: idNumber, Provider: "manual", Approved: false,
	}); err != nil {
		t.Fatalf("a pending submission should be allowed: %v", err)
	}
}

func TestCertificationRequiredSetting(t *testing.T) {
	s := certStore(t)
	ctx := context.Background()
	if on, err := s.CertificationRequired(ctx); err != nil || on {
		t.Fatalf("default = %v/%v, want false", on, err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO system_settings(key,value) VALUES('certification_required','true'::jsonb) ON CONFLICT (key) DO UPDATE SET value='true'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if on, err := s.CertificationRequired(ctx); err != nil || !on {
		t.Fatalf("after enabling = %v/%v, want true", on, err)
	}
}
