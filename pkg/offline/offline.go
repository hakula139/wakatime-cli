package offline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"github.com/wakatime/wakatime-cli/pkg/api"
	"github.com/wakatime/wakatime-cli/pkg/apikey"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
	"github.com/wakatime/wakatime-cli/pkg/ini"
	"github.com/wakatime/wakatime-cli/pkg/log"
	"github.com/wakatime/wakatime-cli/pkg/vipertools"

	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

const (
	// dbFilename is the default bolt db filename.
	dbFilename = "offline_heartbeats.bdb"
	// dbBucket is the standard bolt db bucket name.
	dbBucket = "heartbeats"
	// maxRequeueAttempts defines the maximum number of attempts to requeue heartbeats,
	// which could not successfully be sent to the WakaTime API.
	maxRequeueAttempts = 3
	// PrintMaxDefault is the default maximum number of heartbeats to print.
	PrintMaxDefault = 10
	// RateLimitDefaultSeconds is the default seconds between sending heartbeats
	// to the API. If not enough time has passed, heartbeats are saved to the offline queue.
	RateLimitDefaultSeconds = 120
	// SendLimit is the maximum number of heartbeats, which will be sent at once
	// to the WakaTime API.
	SendLimit = 10
	// SyncMaxDefault is the default maximum number of heartbeats from the
	// offline queue, which will be synced upon sending heartbeats to the API.
	SyncMaxDefault = 1000
	// readDedupeWindowSeconds defines the max distance between two heartbeats
	// for the same entity to be considered duplicates during read cleanup.
	readDedupeWindowSeconds = 1
)

// ErrUnreplayableRoute reports that offline storage cannot preserve a heartbeat's API route.
var ErrUnreplayableRoute = errors.New("offline queue cannot preserve API routing")

// WithQueue initializes and returns a heartbeat handle option, which can be
// used in a heartbeat processing pipeline for automatic handling of failures
// of heartbeat sending to the API. Upon inability to send due to missing or
// failing connection to API, failed sending or errors returned by API, the
// replayable heartbeats are stored in a DB for retry on the next sync.
func WithQueue(filepath string, routing apikey.Config) heartbeat.HandleOption {
	retry := queueForRetry(filepath, routing)
	unsent := QueueUnsent(filepath, routing)

	return func(next heartbeat.Handle) heartbeat.Handle {
		return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			if len(hh) == 0 {
				return nil, nil
			}

			results, sendErr := next(ctx, hh)

			var (
				backoffErr api.ErrBackoff
				queueErr   error
			)

			if errors.As(sendErr, &backoffErr) {
				_, queueErr = unsent(ctx, hh)
			} else {
				_, queueErr = handleResults(ctx, retry, results, hh)
			}

			if queueErr != nil {
				return nil, errors.Join(sendErr, fmt.Errorf("failed to queue unconfirmed heartbeats: %w", queueErr))
			}

			return results, sendErr
		}
	}
}

func queueForRetry(filepath string, routing apikey.Config) heartbeat.Handle {
	return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		// An unconfirmed destination must not replay to another destination that may have succeeded.
		groups := make([][]heartbeat.Heartbeat, len(hh))
		for i := range hh {
			groups[i] = hh[i : i+1]
		}

		return nil, pushReplayableHeartbeats(ctx, filepath, routing, groups)
	}
}

// QueueUnsent stores processed heartbeats that have not been sent to any selected destination.
func QueueUnsent(filepath string, routing apikey.Config) heartbeat.Handle {
	return func(ctx context.Context, hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
		groups, err := groupByWirePayload(hh)
		if err != nil {
			return nil, err
		}

		return nil, pushReplayableHeartbeats(ctx, filepath, routing, groups)
	}
}

func groupByWirePayload(hh []heartbeat.Heartbeat) ([][]heartbeat.Heartbeat, error) {
	groups := make([][]heartbeat.Heartbeat, 0, len(hh))
	index := make(map[string]int, len(hh))

	for _, h := range hh {
		payload, err := json.Marshal(h)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal heartbeat for offline storage: %w", err)
		}

		key := string(payload)
		if i, ok := index[key]; ok {
			groups[i] = append(groups[i], h)
		} else {
			index[key] = len(groups)
			groups = append(groups, []heartbeat.Heartbeat{h})
		}
	}

	return groups, nil
}

func pushReplayableHeartbeats(
	ctx context.Context,
	filepath string,
	routing apikey.Config,
	groups [][]heartbeat.Heartbeat,
) error {
	var (
		replayable []heartbeat.Heartbeat
		rejected   int
		routingErr error
	)

	for _, group := range groups {
		h := group[0]
		if routing.CanReplay(ctx, h, group[1:]...) {
			replayable = append(replayable, h)
		} else {
			rejected++
		}
	}

	if rejected > 0 {
		routingErr = fmt.Errorf("%w for %d heartbeat(s), send with --disable-offline", ErrUnreplayableRoute, rejected)
	}

	if len(replayable) == 0 {
		return routingErr
	}

	return errors.Join(routingErr, pushHeartbeatsWithRetry(ctx, filepath, dbBucket, replayable))
}

// QueueFilepath returns the path for offline queue db file. If
// the resource directory cannot be detected, it defaults to the
// current directory.
func QueueFilepath(ctx context.Context, v *viper.Viper) (string, error) {
	paramFile := vipertools.GetString(v, "offline-queue-file")
	if paramFile != "" {
		p, err := homedir.Expand(paramFile)
		if err != nil {
			return "", fmt.Errorf("failed expanding offline-queue-file param: %s", err)
		}

		return p, nil
	}

	folder, err := ini.WakaResourcesDir(ctx)
	if err != nil {
		return dbFilename, fmt.Errorf("failed getting resource directory, defaulting to current directory: %s", err)
	}

	return filepath.Join(folder, dbFilename), nil
}

// WithSync initializes and returns a heartbeat handle option, which
// can be used in a heartbeat processing pipeline to pop heartbeats
// from offline queue and send the heartbeats to WakaTime API.
func WithSync(filepath string, syncLimit int, routing apikey.Config) heartbeat.HandleOption {
	return func(next heartbeat.Handle) heartbeat.Handle {
		return func(ctx context.Context, _ []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			// logger.Debugf("execute offline sync with file %s", filepath)
			err := Sync(ctx, filepath, syncLimit, routing)(next)
			if err != nil {
				return nil, fmt.Errorf("failed to sync offline heartbeats: %w", err)
			}

			return nil, nil
		}
	}
}

// Sync returns a function to send queued heartbeats to the WakaTime API.
func Sync(
	ctx context.Context,
	filepath string,
	syncLimit int,
	routing apikey.Config,
) func(next heartbeat.Handle) error {
	return func(next heartbeat.Handle) error {
		var (
			alreadySent int
			run         int
		)

		logger := log.Extract(ctx)
		retry := queueForRetry(filepath, routing)

		for {
			run++

			if alreadySent >= syncLimit {
				break
			}

			var num = SendLimit

			if alreadySent+SendLimit > syncLimit {
				num = syncLimit - alreadySent
				alreadySent += num
			}

			hh, err := popHeartbeats(ctx, filepath, num)
			if err != nil {
				return fmt.Errorf("failed to fetch heartbeat from offline queue: %w", err)
			}

			if len(hh) == 0 {
				logger.Debugln("no queued heartbeats ready for sending")

				break
			}

			logger.Debugf("send %d heartbeats on sync run %d", len(hh), run)

			routed := routing.Apply(ctx, hh)
			results, sendErr := next(ctx, routed)

			stopSending, queueErr := handleResults(ctx, retry, results, routed)
			if queueErr != nil {
				return errors.Join(sendErr, fmt.Errorf("failed to handle heartbeats api results: %w", queueErr))
			}

			if sendErr != nil {
				return sendErr
			}

			if stopSending {
				break
			}

			alreadySent += len(hh)
		}

		return nil
	}
}

func handleResults(
	ctx context.Context,
	queue heartbeat.Handle,
	results []heartbeat.Result,
	hh []heartbeat.Heartbeat,
) (bool, error) {
	var (
		toRequeue   []heartbeat.Heartbeat
		stopSending bool
	)

	logger := log.Extract(ctx)
	handled := make(map[resultID]int, len(results))

	for n, result := range results {
		h, ok := resultHeartbeat(result, hh, n)
		if !ok {
			stopSending = true

			break
		}

		handled[routedResultID(h)]++

		if result.Status == http.StatusBadRequest {
			serialized, jsonErr := json.Marshal(h)
			if jsonErr != nil {
				logger.Warnf(
					"failed to json marshal heartbeat: %s. heartbeat: %#v",
					jsonErr,
					h,
				)
			}

			logger.Debugf("heartbeat result status bad request: %s", string(serialized))

			continue
		}

		if result.Status < http.StatusOK || result.Status > 299 {
			toRequeue = append(toRequeue, h)

			logger.Debugf("heartbeat %d has invalid status code %d: %s", n, result.Status, strings.Join(result.Errors, ", "))
		}
	}

	leftovers := missingResultHeartbeats(hh, handled)
	if len(leftovers) > 0 {
		logger.Warnf("missing %d results from api.", len(leftovers))

		toRequeue = append(toRequeue, leftovers...)
	}

	if len(toRequeue) == 0 {
		return stopSending, nil
	}

	logger.Debugf("pushing %d heartbeat(s) with missing or invalid results to queue", len(toRequeue))
	_, err := queue(ctx, toRequeue)

	return true, err
}

func resultHeartbeat(result heartbeat.Result, hh []heartbeat.Heartbeat, n int) (heartbeat.Heartbeat, bool) {
	if result.Heartbeat.Entity != "" || result.Heartbeat.Time != 0 {
		return result.Heartbeat, true
	}

	if n >= len(hh) {
		return heartbeat.Heartbeat{}, false
	}

	return hh[n], true
}

type resultID struct {
	id  string
	url string
	key string
}

func routedResultID(h heartbeat.Heartbeat) resultID {
	return resultID{id: h.ID(), url: h.APIURL, key: h.APIKey}
}

func missingResultHeartbeats(hh []heartbeat.Heartbeat, handled map[resultID]int) []heartbeat.Heartbeat {
	var missing []heartbeat.Heartbeat

	for _, h := range hh {
		id := routedResultID(h)
		if handled[id] > 0 {
			handled[id]--
			continue
		}

		missing = append(missing, h)
	}

	return missing
}

func popHeartbeats(ctx context.Context, filepath string, limit int) (queued []heartbeat.Heartbeat, err error) {
	defer func() {
		recoverDBPanic(filepath, &err, recover())
	}()

	db, close, err := openDB(ctx, filepath)
	if err != nil {
		return nil, err
	}

	defer close()

	logger := log.Extract(ctx)

	tx, err := db.Begin(true)
	if err != nil {
		return nil, fmt.Errorf("failed to start db transaction: %w", err)
	}

	defer func() {
		if tx == nil {
			return
		}

		if err := tx.Rollback(); err != nil {
			logger.Errorf("failed to rollback transaction: %s", err)
		}
	}()

	queue := NewQueue(tx)

	if err := queue.PromoteAI(time.Now()); err != nil {
		return nil, fmt.Errorf("failed to promote pending ai heartbeats: %w", err)
	}

	_, err = queue.DeleteDuplicates()
	if err != nil {
		logger.Errorf("failed to delete duplicate offline heartbeats: %s", err)

		return nil, err
	}

	queued, err = queue.PopMany(limit)
	if err != nil {
		return nil, fmt.Errorf("failed to pop heartbeat(s) from queue: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit db transaction: %w", err)
	}

	tx = nil

	return queued, nil
}

func pushHeartbeatsWithRetry(ctx context.Context, filepath, bucket string, hh []heartbeat.Heartbeat) error {
	var (
		count int
		err   error
	)

	logger := log.Extract(ctx)

	for {
		if count >= maxRequeueAttempts {
			serialized, jsonErr := json.Marshal(hh)
			if jsonErr != nil {
				logger.Warnf("failed to json marshal heartbeats: %s. heartbeats: %#v", jsonErr, hh)
			}

			return fmt.Errorf(
				"abort requeuing after %d unsuccessful attempts: %s. heartbeats: %s",
				count,
				err,
				string(serialized),
			)
		}

		err = pushHeartbeats(ctx, filepath, bucket, hh)
		if err != nil {
			count++

			var erropen ErrOpenDB
			if errors.As(err, &erropen) && erropen.Reset {
				logger.Warnf("offline db was reset after corruption: %s", erropen)

				continue
			}

			sleepSeconds := math.Pow(2, float64(count))

			time.Sleep(time.Duration(sleepSeconds) * time.Second)

			continue
		}

		break
	}

	return nil
}

func pushHeartbeats(ctx context.Context, filepath, bucket string, hh []heartbeat.Heartbeat) (err error) {
	defer func() {
		recoverDBPanic(filepath, &err, recover())
	}()

	db, close, err := openDB(ctx, filepath)
	if err != nil {
		return err
	}

	defer close()

	logger := log.Extract(ctx)

	tx, err := db.Begin(true)
	if err != nil {
		return fmt.Errorf("failed to start db transaction: %w", err)
	}

	defer func() {
		if tx == nil {
			return
		}

		if err := tx.Rollback(); err != nil {
			logger.Errorf("failed to rollback transaction: %s", err)
		}
	}()

	queue := NewQueue(tx)
	queue.Bucket = bucket

	err = queue.PushMany(hh)
	if err != nil {
		return fmt.Errorf("failed to push heartbeat(s) to queue: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit db transaction: %w", err)
	}

	tx = nil

	return nil
}

// CountHeartbeats returns the total number of heartbeats in the offline db.
func CountHeartbeats(ctx context.Context, filepath string) (count int, err error) {
	defer func() {
		recoverDBPanic(filepath, &err, recover())
	}()

	db, close, err := openDB(ctx, filepath)
	if err != nil {
		return 0, err
	}

	defer close()

	tx, err := db.Begin(true)
	if err != nil {
		return 0, fmt.Errorf("failed to start db transaction: %w", err)
	}

	logger := log.Extract(ctx)

	defer func() {
		err := tx.Rollback()
		if err != nil {
			logger.Errorf("failed to rollback transaction: %s", err)
		}
	}()

	queue := NewQueue(tx)

	count, err = queue.Count()
	if err != nil {
		return 0, fmt.Errorf("failed to count heartbeats: %w", err)
	}

	queue.Bucket = aiPendingBucket

	pendingCount, err := queue.Count()
	if err != nil {
		return 0, fmt.Errorf("failed to count pending ai heartbeats: %w", err)
	}

	return count + pendingCount, nil
}

// ReadHeartbeats reads the informed heartbeats in the offline db.
func ReadHeartbeats(ctx context.Context, filepath string, limit int) (hh []heartbeat.Heartbeat, err error) {
	defer func() {
		recoverDBPanic(filepath, &err, recover())
	}()

	db, close, err := openDB(ctx, filepath)
	if err != nil {
		return nil, err
	}

	defer close()

	tx, err := db.Begin(true)
	if err != nil {
		return nil, fmt.Errorf("failed to start db transaction: %w", err)
	}

	queue := NewQueue(tx)
	logger := log.Extract(ctx)

	defer func() {
		if tx == nil {
			return
		}

		if err := tx.Rollback(); err != nil {
			logger.Warnf("failed to rollback transaction: %s", err)
		}
	}()

	hh, err = queue.ReadMany(limit)
	if err != nil {
		logger.Errorf("failed to read offline heartbeats: %s", err)

		return nil, err
	}

	queue.Bucket = aiPendingBucket

	pending, err := queue.ReadMany(limit)
	if err != nil {
		return nil, fmt.Errorf("failed to read pending ai heartbeats: %w", err)
	}

	hh = append(hh, pending...)
	sort.SliceStable(hh, func(i, j int) bool {
		return hh[i].Time < hh[j].Time
	})

	if limit >= 0 && len(hh) > limit {
		hh = hh[:limit]
	}

	return hh, nil
}

func recoverDBPanic(dbFilepath string, err *error, recovered any) {
	if recovered != nil {
		*err = resetCorruptDBError(dbFilepath, fmt.Errorf("panicked: %v", recovered))
	}
}

// openDB opens a connection to the offline db.
// It returns the pointer to bolt.DB, a function to close the connection and an error.
// Although named parameters should be avoided, this func uses them to access inside the deferred function and set an error.
func openDB(ctx context.Context, filepath string) (db *bolt.DB, _ func(), err error) {
	defer func() {
		if r := recover(); r != nil {
			err = resetCorruptDBError(filepath, fmt.Errorf("panicked: %v", r))
		}
	}()

	db, err = bolt.Open(filepath, 0644, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		if isCorruptDBError(err) {
			return nil, nil, resetCorruptDBError(filepath, fmt.Errorf("failed to open db file: %w", err))
		}

		return nil, nil, fmt.Errorf("failed to open db file: %w", err)
	}

	return db, func() {
		logger := log.Extract(ctx)

		// recover from panic when closing db
		defer func() {
			if r := recover(); r != nil {
				logger.Warnf("panicked: failed to close db file: %v", r)
			}
		}()

		if err := db.Close(); err != nil {
			logger.Debugf("failed to close db file: %s", err)
		}
	}, err
}

func isCorruptDBError(err error) bool {
	return errors.Is(err, bolterrors.ErrInvalid) ||
		errors.Is(err, bolterrors.ErrVersionMismatch) ||
		errors.Is(err, bolterrors.ErrChecksum)
}

func resetCorruptDBError(dbFilepath string, cause error) ErrOpenDB {
	backupFilepath, err := resetCorruptDB(dbFilepath)
	if err != nil {
		return ErrOpenDB{Err: fmt.Errorf("%w; failed to move corrupt db file: %v", cause, err)}
	}

	if backupFilepath == "" {
		return ErrOpenDB{Err: fmt.Errorf("%w; corrupt db file already removed", cause), Reset: true}
	}

	return ErrOpenDB{
		Err:            fmt.Errorf("%w; moved corrupt db file to %q", cause, backupFilepath),
		BackupFilepath: backupFilepath,
		Reset:          true,
	}
}

func resetCorruptDB(dbFilepath string) (string, error) {
	backupFilepath := fmt.Sprintf(
		"%s.corrupt.%s",
		dbFilepath,
		time.Now().UTC().Format("20060102T150405.000000000Z"),
	)

	err := os.Rename(dbFilepath, backupFilepath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	return backupFilepath, nil
}

// Queue is a db client to temporarily store heartbeats in bolt db, in case heartbeat
// sending to wakatime api is not possible. Transaction handling is left to the user
// via the passed in transaction.
type Queue struct {
	Bucket string
	tx     *bolt.Tx
}

// NewQueue creates a new instance of Queue.
func NewQueue(tx *bolt.Tx) *Queue {
	return &Queue{
		Bucket: dbBucket,
		tx:     tx,
	}
}

// Count returns the total number of heartbeats in the offline db.
func (q *Queue) Count() (int, error) {
	b, err := q.tx.CreateBucketIfNotExists([]byte(q.Bucket))
	if err != nil {
		return 0, fmt.Errorf("failed to create/load bucket: %w", err)
	}

	return b.Stats().KeyN, nil
}

// PopMany retrieves and deletes up to limit heartbeats from db, newest first.
func (q *Queue) PopMany(limit int) ([]heartbeat.Heartbeat, error) {
	b, err := q.tx.CreateBucketIfNotExists([]byte(q.Bucket))
	if err != nil {
		return nil, fmt.Errorf("failed to create/load bucket: %w", err)
	}

	var (
		heartbeats []heartbeat.Heartbeat
		ids        []string
	)

	// load values
	c := b.Cursor()

	// Heartbeat ids are prefixed with their timestamp, so reverse key order is
	// descending heartbeat timestamp order.
	for key, value := c.Last(); key != nil; key, value = c.Prev() {
		if len(heartbeats) >= limit {
			break
		}

		var h heartbeat.Heartbeat

		err := json.Unmarshal(value, &h)
		if err != nil {
			return nil, fmt.Errorf("failed to json unmarshal heartbeat data: %w", err)
		}

		heartbeats = append(heartbeats, h)
		ids = append(ids, string(key))
	}

	for _, id := range ids {
		if err := b.Delete([]byte(id)); err != nil {
			return nil, fmt.Errorf("failed to delete key %q: %w", id, err)
		}
	}

	return heartbeats, nil
}

// PushMany stores the provided heartbeats in the db.
func (q *Queue) PushMany(hh []heartbeat.Heartbeat) error {
	b, err := q.tx.CreateBucketIfNotExists([]byte(q.Bucket))
	if err != nil {
		return fmt.Errorf("failed to create/load bucket: %w", err)
	}

	for _, h := range hh {
		data, err := json.Marshal(h)
		if err != nil {
			return fmt.Errorf("failed to json marshal heartbeat: %w", err)
		}

		err = b.Put([]byte(h.ID()), data)
		if err != nil {
			return fmt.Errorf("failed to store heartbeat with id %q: %w", h.ID(), err)
		}
	}

	return nil
}

// ReadMany reads heartbeats from db without deleting them.
func (q *Queue) ReadMany(limit int) ([]heartbeat.Heartbeat, error) {
	b, err := q.tx.CreateBucketIfNotExists([]byte(q.Bucket))
	if err != nil {
		return nil, fmt.Errorf("failed to create/load bucket: %w", err)
	}

	var heartbeats = make([]heartbeat.Heartbeat, 0)

	// load values
	c := b.Cursor()

	for key, value := c.First(); key != nil; key, value = c.Next() {
		if len(heartbeats) >= limit {
			break
		}

		var h heartbeat.Heartbeat

		err := json.Unmarshal(value, &h)
		if err != nil {
			return nil, fmt.Errorf("failed to json unmarshal heartbeat data: %w", err)
		}

		heartbeats = append(heartbeats, h)
	}

	return heartbeats, nil
}

// DeleteDuplicates cleanups and deletes duplicate heartbeats from the offline db.
func (q *Queue) DeleteDuplicates() (int, error) {
	b, err := q.tx.CreateBucketIfNotExists([]byte(q.Bucket))
	if err != nil {
		return 0, fmt.Errorf("failed to create/load bucket: %w", err)
	}

	kept := make(map[string][]float64)
	deleted := 0

	c := b.Cursor()
	for key, value := c.First(); key != nil; key, value = c.Next() {
		var h heartbeat.Heartbeat

		err := json.Unmarshal(value, &h)
		if err != nil {
			return 0, fmt.Errorf("failed to json unmarshal heartbeat data: %w", err)
		}

		if h.Category == heartbeat.AICodingCategory.String() {
			continue
		}

		isDuplicate := false

		for _, keptTime := range kept[h.Entity] {
			if math.Abs(h.Time-keptTime) <= readDedupeWindowSeconds {
				isDuplicate = true
				break
			}
		}

		if isDuplicate {
			if err := c.Delete(); err != nil {
				return 0, fmt.Errorf("failed to delete duplicate heartbeat with key %q: %w", string(key), err)
			}

			deleted++

			continue
		}

		kept[h.Entity] = append(kept[h.Entity], h.Time)
	}

	return deleted, nil
}
