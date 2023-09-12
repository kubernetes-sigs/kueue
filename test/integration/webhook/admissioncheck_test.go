package webhook

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AdmissionCheck Webhook", func() {
	ginkgo.When("Creating a AdmissionCheck", func() {

		ginkgo.DescribeTable("Defaulting on creating", func(ac, wantAC kueue.AdmissionCheck) {
			gomega.Expect(k8sClient.Create(ctx, &ac)).Should(gomega.Succeed())
			defer func() {
				util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, &ac, true)
			}()
			gomega.Expect(ac).To(gomega.BeComparableTo(wantAC,
				cmpopts.IgnoreTypes(kueue.AdmissionCheckStatus{}),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields")))
		},
			ginkgo.Entry("All defaults",
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				kueue.AdmissionCheck{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Spec: kueue.AdmissionCheckSpec{
						RetryDelayMinutes: ptr.To[int64](15),
						PreemptionPolicy:  ptr.To(kueue.Anytime),
					},
				},
			),
		)
	})
})
