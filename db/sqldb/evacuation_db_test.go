package sqldb_test

import (
	"time"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/bbs/models/test/model_helpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Evacuation", func() {
	var (
		actualLRP *models.ActualLRP
		guid      string
		index     int32
	)

	BeforeEach(func() {
		guid = "some-guid"
		index = int32(1)
		actualLRP = model_helpers.NewValidActualLRP(guid, index)
		actualLRP.CrashCount = 0
		actualLRP.CrashReason = ""
		actualLRP.Since = fakeClock.Now().Truncate(time.Microsecond).UnixNano()
		actualLRP.ModificationTag = models.ModificationTag{}
		actualLRP.ModificationTag.Increment()
		actualLRP.ModificationTag.Increment()

		Expect(sqlDB.CreateUnclaimedActualLRP(logger, &actualLRP.ActualLRPKey)).To(Succeed())
		Expect(sqlDB.ClaimActualLRP(logger, guid, index, &actualLRP.ActualLRPInstanceKey)).To(Succeed())
		Expect(sqlDB.StartActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo)).To(Succeed())
	})

	Describe("EvacuateActualLRP", func() {
		var ttl uint64

		BeforeEach(func() {
			ttl = 60

			expireTime := fakeClock.Now().Add(time.Duration(ttl) * time.Second)
			_, err := db.Exec(
				`UPDATE actual_lrps SET evacuating = ?, expire_time = ?
			    WHERE process_guid = ? AND instance_index = ? AND evacuating = ?`,
				true,
				expireTime,
				actualLRP.ProcessGuid,
				actualLRP.Index,
				false,
			)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the something about the actual LRP has changed", func() {
			BeforeEach(func() {
				fakeClock.IncrementBySeconds(5)
				actualLRP.Since = fakeClock.Now().Truncate(time.Microsecond).UnixNano()
				actualLRP.ModificationTag.Increment()
			})

			Context("when the lrp key changes", func() {
				BeforeEach(func() {
					actualLRP.Domain = "some-other-domain"
				})

				It("persists the evacuating lrp in sqldb", func() {
					err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
					Expect(err).NotTo(HaveOccurred())

					actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).NotTo(HaveOccurred())
					Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
				})
			})

			Context("when the instance key changes", func() {
				BeforeEach(func() {
					actualLRP.ActualLRPInstanceKey.InstanceGuid = "i am different here me roar"
				})

				It("persists the evacuating lrp in etcd", func() {
					err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
					Expect(err).NotTo(HaveOccurred())

					actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).NotTo(HaveOccurred())
					Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
				})
			})

			Context("when the netinfo changes", func() {
				BeforeEach(func() {
					actualLRP.ActualLRPNetInfo.Ports = []*models.PortMapping{
						models.NewPortMapping(6666, 7777),
					}
				})

				It("persists the evacuating lrp in etcd", func() {
					err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
					Expect(err).NotTo(HaveOccurred())

					actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).NotTo(HaveOccurred())
					Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
				})
			})
		})

		Context("when the evacuating actual lrp does not exist", func() {
			Context("because the record is deleted", func() {
				BeforeEach(func() {
					_, err := db.Exec("DELETE FROM actual_lrps WHERE process_guid = ? AND instance_index = ? AND evacuating = ?", actualLRP.ProcessGuid, actualLRP.Index, true)
					Expect(err).NotTo(HaveOccurred())

					actualLRP.CrashCount = 0
					actualLRP.CrashReason = ""
					actualLRP.Since = fakeClock.Now().Truncate(time.Microsecond).UnixNano()
				})

				It("creates the evacuating actual lrp", func() {
					err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
					Expect(err).NotTo(HaveOccurred())

					actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).NotTo(HaveOccurred())
					Expect(actualLRPGroup.Evacuating.ModificationTag.Epoch).NotTo(BeNil())
					Expect(actualLRPGroup.Evacuating.ModificationTag.Index).To(BeEquivalentTo((0)))

					actualLRPGroup.Evacuating.ModificationTag = actualLRP.ModificationTag
					Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
				})

				Context("with an invalid net info", func() {
					BeforeEach(func() {
						actualLRP.ActualLRPNetInfo = models.EmptyActualLRPNetInfo()
					})

					It("returns an error", func() {
						err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
						Expect(err).To(HaveOccurred())
					})
				})
			})

			Context("because the record has expired", func() {
				BeforeEach(func() {
					fakeClock.Increment(61 * time.Second)

					actualLRP.CrashCount = 0
					actualLRP.CrashReason = ""
					actualLRP.Since = fakeClock.Now().Truncate(time.Microsecond).UnixNano()
				})

				It("updates the expired evacuating actual lrp", func() {
					err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
					Expect(err).NotTo(HaveOccurred())

					actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).NotTo(HaveOccurred())
					Expect(actualLRPGroup.Evacuating.ModificationTag.Epoch).NotTo(BeNil())
					Expect(actualLRPGroup.Evacuating.ModificationTag.Index).To(BeEquivalentTo((0)))

					actualLRPGroup.Evacuating.ModificationTag = actualLRP.ModificationTag
					Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
				})

				Context("with an invalid net info", func() {
					BeforeEach(func() {
						actualLRP.ActualLRPNetInfo = models.EmptyActualLRPNetInfo()
					})

					It("returns an error", func() {
						err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
						Expect(err).To(HaveOccurred())
					})
				})
			})
		})

		Context("when the fetched lrp has not changed", func() {
			It("does not update the record", func() {
				err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
				Expect(err).NotTo(HaveOccurred())

				actualLRPGroup, err := sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
				Expect(err).NotTo(HaveOccurred())
				Expect(actualLRPGroup.Evacuating).To(BeEquivalentTo(actualLRP))
			})
		})

		Context("when deserializing the data fails", func() {
			BeforeEach(func() {
				_, err := db.Exec(`
						UPDATE actual_lrps SET net_info = ?
						WHERE process_guid = ? AND instance_index = ? AND evacuating = ?
					`,
					"garbage", actualLRP.ProcessGuid, actualLRP.Index, true)
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns an error", func() {
				err := sqlDB.EvacuateActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey, &actualLRP.ActualLRPNetInfo, ttl)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("RemoveEvacuatingActualLRP", func() {
		Context("when there is an evacuating actualLRP", func() {
			BeforeEach(func() {
				expireTime := fakeClock.Now().Add(5 * time.Second)
				_, err := db.Exec("UPDATE actual_lrps SET evacuating = ?, expire_time = ? WHERE process_guid = ? AND instance_index = ? AND evacuating = ?", true, expireTime, actualLRP.ProcessGuid, actualLRP.Index, false)
				Expect(err).NotTo(HaveOccurred())
			})

			It("removes the evacuating actual LRP", func() {
				err := sqlDB.RemoveEvacuatingActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey)
				Expect(err).ToNot(HaveOccurred())

				_, err = sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(models.ErrResourceNotFound))
			})

			Context("when the actual lrp instance key is not the same", func() {
				BeforeEach(func() {
					actualLRP.CellId = "a different cell"
				})

				It("returns a ErrActualLRPCannotBeRemoved error", func() {
					err := sqlDB.RemoveEvacuatingActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey)
					Expect(err).To(Equal(models.ErrActualLRPCannotBeRemoved))
				})
			})

			Context("when the actualLRP is expired", func() {
				BeforeEach(func() {
					expireTime := fakeClock.Now()
					_, err := db.Exec("UPDATE actual_lrps SET expire_time = ? WHERE process_guid = ? AND instance_index = ? AND evacuating = ?", expireTime, actualLRP.ProcessGuid, actualLRP.Index, false)
					Expect(err).NotTo(HaveOccurred())
				})

				It("does not return an error", func() {
					err := sqlDB.RemoveEvacuatingActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey)
					Expect(err).NotTo(HaveOccurred())

					_, err = sqlDB.ActualLRPGroupByProcessGuidAndIndex(logger, guid, index)
					Expect(err).To(HaveOccurred())
					Expect(err).To(Equal(models.ErrResourceNotFound))
				})
			})
		})

		Context("when the actualLRP does not exist", func() {
			It("does not return an error", func() {
				err := sqlDB.RemoveEvacuatingActualLRP(logger, &actualLRP.ActualLRPKey, &actualLRP.ActualLRPInstanceKey)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})
