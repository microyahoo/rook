package bucket

import (
	"github.com/ceph/go-ceph/rgw/admin"
	"github.com/pkg/errors"
)

func (p *Provisioner) bucketExists(name string) (bool, error) {
	_, err := p.adminOpsClient.GetBucketInfo(p.clusterInfo.Context, admin.Bucket{Bucket: name})
	if err != nil {
		if errors.Is(err, admin.ErrNoSuchBucket) {
			return false, nil
		}
		return false, errors.Wrapf(err, "failed to get ceph bucket %q", name)
	}
	return true, nil
}

// Create a Ceph user based on the passed-in name or a generated name. Return the
// accessKeys and set user name and keys in receiver.
func (p *Provisioner) createCephUser(username string) (accKey string, secKey string, err error) {
	accKey, secKey, err = p.getCephUser(username)
	if err != nil {
		if errors.Is(err, admin.ErrNoSuchUser) {
			p.cephUserName = username

			logger.Infof("creating Ceph os user %q", username)
			userConfig := admin.User{
				ID:          username,
				DisplayName: p.cephUserName,
			}
			u, err := p.adminOpsClient.CreateUser(p.clusterInfo.Context, userConfig)
			if err != nil {
				return "", "", errors.Wrapf(err, "failed to create ceph object user %v", userConfig.ID)
			}
			logger.Infof("successfully created Ceph user %q with access keys", username)
			return u.Keys[0].AccessKey, u.Keys[0].SecretKey, nil
		} else {
			return "", "", errors.Wrapf(err, "failed to get ceph user %q", username)
		}
	}
	return accKey, secKey, nil
}

// Get a Ceph user based on the passed-in name or a generated name. Return the
// accessKeys and set user name and keys in receiver.
func (p *Provisioner) getCephUser(username string) (accKey string, secKey string, err error) {
	if len(username) == 0 {
		return "", "", errors.Wrap(err, "no user name provided")
	}
	p.cephUserName = username

	logger.Infof("getting Ceph user %q", username)
	userConfig := admin.User{
		ID:          username,
		DisplayName: p.cephUserName,
	}

	var u admin.User
	u, err = p.adminOpsClient.GetUser(p.clusterInfo.Context, userConfig)
	if err != nil {
		return "", "", errors.Wrapf(err, "failed to get ceph user %q", username)
	}

	logger.Infof("successfully get Ceph user %q with access keys", username)
	return u.Keys[0].AccessKey, u.Keys[0].SecretKey, nil
}

func (p *Provisioner) genUserName(obcName, obcNamespace string) string {
	// user name can be deterministically generated by obc name and namespace
	// since there can't be 2 obcs in the same namespace with the same name, this will not collide
	return "obc-" + obcNamespace + "-" + obcName
}

// Delete the bucket created by OBC with help of radosgw-admin commands
func (p *Provisioner) deleteOBCResource(bucketName string, ignoreUser bool) error {

	logger.Infof("deleting bucket %q from Ceph user %q ", bucketName, p.cephUserName)
	if len(bucketName) > 0 {
		// delete bucket with purge option to remove all objects
		thePurge := true
		err := p.adminOpsClient.RemoveBucket(p.clusterInfo.Context, admin.Bucket{Bucket: bucketName, PurgeObject: &thePurge})
		if err == nil {
			logger.Infof("bucket %q successfully deleted", bucketName)
		} else if errors.Is(err, admin.ErrNoSuchBucket) {
			// opinion: "not found" is not an error
			logger.Infof("bucket %q does not exist", bucketName)
		} else if errors.Is(err, admin.ErrNoSuchKey) {
			// ceph might return NoSuchKey than NoSuchBucket when the target bucket does not exist.
			// then we can use GetBucketInfo() to judge the existence of the bucket.
			// see: https://github.com/ceph/ceph/pull/44413
			_, err2 := p.adminOpsClient.GetBucketInfo(p.clusterInfo.Context, admin.Bucket{Bucket: bucketName, PurgeObject: &thePurge})
			if errors.Is(err2, admin.ErrNoSuchBucket) {
				logger.Infof("bucket info %q does not exist", bucketName)
			} else {
				return errors.Wrapf(err, "failed to delete bucket %q (could not get bucket info)", bucketName)
			}
		} else {
			return errors.Wrapf(err, "failed to delete bucket %q", bucketName)
		}
	}
	if !ignoreUser && len(p.cephUserName) > 0 {
		err := p.adminOpsClient.RemoveUser(p.clusterInfo.Context, admin.User{ID: p.cephUserName})
		if err != nil {
			if errors.Is(err, admin.ErrNoSuchUser) {
				logger.Warningf("user %q does not exist, nothing to delete. %v", p.cephUserName, err)
			}
			logger.Warningf("failed to delete user %q. %v", p.cephUserName, err)
		} else {
			logger.Infof("user %q successfully deleted", p.cephUserName)
		}
	}
	return nil
}
