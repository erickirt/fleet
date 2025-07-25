package logging

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/firehose"
	"github.com/aws/aws-sdk-go-v2/service/firehose/types"
	"github.com/fleetdm/fleet/v4/server/logging/mock"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
)

var (
	logs = []json.RawMessage{
		json.RawMessage(`{"foo":"bar"}`),
		json.RawMessage(`{"flim":"flam"}`),
		json.RawMessage(`{"jim":"jom"}`),
	}
	logsWithNewlines = []json.RawMessage{
		json.RawMessage(`{"foo":"bar"}` + "\n"),
		json.RawMessage(`{"flim":"flam"}` + "\n"),
		json.RawMessage(`{"jim":"jom"}` + "\n"),
	}
)

func makeFirehoseWriterWithMock(client FirehoseAPI, stream string) *firehoseLogWriter {
	return &firehoseLogWriter{
		client: client,
		stream: stream,
		logger: log.NewNopLogger(),
	}
}

func getLogsFromInput(input *firehose.PutRecordBatchInput) []json.RawMessage {
	var logs []json.RawMessage
	for _, record := range input.Records {
		logs = append(logs, record.Data)
	}
	return logs
}

func TestFirehoseNonRetryableFailure(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	putFunc := func(context.Context, *firehose.PutRecordBatchInput, ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		return nil, errors.New("generic error")
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.Error(t, err)
	assert.Equal(t, 1, callCount)
}

func TestFirehoseRetryableFailure(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	putFunc := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Equal(t, logsWithNewlines, getLogsFromInput(input))
		assert.Equal(t, "foobar", *input.DeliveryStreamName)
		if callCount < 3 {
			return nil, &types.ServiceUnavailableException{}
		}
		// Returning a non-retryable error earlier helps keep
		// this test faster
		return nil, errors.New("generic error")
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.Error(t, err)
	assert.Equal(t, 3, callCount)
}

func TestFirehoseNormalPut(t *testing.T) {
	ctx := context.Background()
	callCount := 0
	putFunc := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Equal(t, logsWithNewlines, getLogsFromInput(input))
		assert.Equal(t, "foobar", *input.DeliveryStreamName)
		return &firehose.PutRecordBatchOutput{
			FailedPutCount: aws.Int32(0),
		}, nil
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)
}

func TestFirehoseSomeFailures(t *testing.T) {
	ctx := context.Background()
	f := &mock.FirehoseMock{}
	callCount := 0

	call3 := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		// final invocation
		callCount += 1
		assert.Equal(t, logsWithNewlines[1:2], getLogsFromInput(input))
		return &firehose.PutRecordBatchOutput{
			FailedPutCount: aws.Int32(0),
		}, nil
	}

	call2 := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		// Set to invoke call3 next time
		f.PutRecordBatchFunc = call3
		callCount += 1
		assert.Equal(t, logsWithNewlines[1:], getLogsFromInput(input))
		return &firehose.PutRecordBatchOutput{
			FailedPutCount: aws.Int32(1),
			RequestResponses: []types.PutRecordBatchResponseEntry{
				{
					ErrorCode: aws.String("error"),
				},
				{
					RecordId: aws.String("foo"),
				},
			},
		}, nil
	}

	call1 := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		// Use call2 function for next call
		f.PutRecordBatchFunc = call2
		callCount += 1
		assert.Equal(t, logsWithNewlines, getLogsFromInput(input))
		return &firehose.PutRecordBatchOutput{
			FailedPutCount: aws.Int32(1),
			RequestResponses: []types.PutRecordBatchResponseEntry{
				{
					RecordId: aws.String("foo"),
				},
				{
					ErrorCode: aws.String("error"),
				},
				{
					ErrorCode: aws.String("error"),
				},
			},
		}, nil
	}
	f.PutRecordBatchFunc = call1
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount)
}

func TestFirehoseFailAllRecords(t *testing.T) {
	ctx := context.Background()
	f := &mock.FirehoseMock{}
	callCount := 0

	f.PutRecordBatchFunc = func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Equal(t, logsWithNewlines, getLogsFromInput(input))
		if callCount < 3 {
			return &firehose.PutRecordBatchOutput{
				FailedPutCount: aws.Int32(1),
				RequestResponses: []types.PutRecordBatchResponseEntry{
					{ErrorCode: aws.String("error")},
					{ErrorCode: aws.String("error")},
					{ErrorCode: aws.String("error")},
				},
			}, nil
		}
		// Make test quicker by returning non-retryable error
		// before all retries are exhausted.
		return nil, errors.New("generic error")
	}

	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.Error(t, err)
	assert.Equal(t, 3, callCount)
}

func TestFirehoseRecordTooBig(t *testing.T) {
	ctx := context.Background()
	newLogs := make([]json.RawMessage, len(logs))
	copy(newLogs, logs)
	newLogs[0] = make(json.RawMessage, firehoseMaxSizeOfRecord+1)
	callCount := 0
	putFunc := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Equal(t, logsWithNewlines[1:], getLogsFromInput(input))
		assert.Equal(t, "foobar", *input.DeliveryStreamName)
		return &firehose.PutRecordBatchOutput{FailedPutCount: aws.Int32(0)}, nil
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, newLogs)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)
}

func TestFirehoseSplitBatchBySize(t *testing.T) {
	ctx := context.Background()
	// Make each record just under 1 MB so that it takes 3 total batches of
	// just under 4 MB each
	logs := make([]json.RawMessage, 12)
	for i := 0; i < len(logs); i++ {
		logs[i] = make(json.RawMessage, firehoseMaxSizeOfRecord-1)
	}
	callCount := 0
	putFunc := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Len(t, getLogsFromInput(input), 4)
		assert.Equal(t, "foobar", *input.DeliveryStreamName)
		return &firehose.PutRecordBatchOutput{FailedPutCount: aws.Int32(0)}, nil
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount)
}

func TestFirehoseSplitBatchByCount(t *testing.T) {
	ctx := context.Background()
	logs := make([]json.RawMessage, 2000)
	for i := 0; i < len(logs); i++ {
		logs[i] = json.RawMessage(`{}`)
	}
	callCount := 0
	putFunc := func(ctx context.Context, input *firehose.PutRecordBatchInput, optFns ...func(*firehose.Options)) (*firehose.PutRecordBatchOutput, error) {
		callCount += 1
		assert.Len(t, getLogsFromInput(input), 500)
		assert.Equal(t, "foobar", *input.DeliveryStreamName)
		return &firehose.PutRecordBatchOutput{FailedPutCount: aws.Int32(0)}, nil
	}
	f := &mock.FirehoseMock{PutRecordBatchFunc: putFunc}
	writer := makeFirehoseWriterWithMock(f, "foobar")
	err := writer.Write(ctx, logs)
	assert.NoError(t, err)
	assert.Equal(t, 4, callCount)
}

func TestFirehoseValidateStreamActive(t *testing.T) {
	describeFunc := func(ctx context.Context, input *firehose.DescribeDeliveryStreamInput, optFns ...func(*firehose.Options)) (*firehose.DescribeDeliveryStreamOutput, error) {
		assert.Equal(t, "test", *input.DeliveryStreamName)
		return &firehose.DescribeDeliveryStreamOutput{
			DeliveryStreamDescription: &types.DeliveryStreamDescription{
				DeliveryStreamStatus: types.DeliveryStreamStatusActive,
			},
		}, nil
	}
	f := &mock.FirehoseMock{DescribeDeliveryStreamFunc: describeFunc}
	writer := makeFirehoseWriterWithMock(f, "test")
	err := writer.validateStream(context.Background())
	assert.NoError(t, err)
	assert.True(t, f.DescribeDeliveryStreamFuncInvoked)
}

func TestFirehoseValidateStreamNotActive(t *testing.T) {
	describeFunc := func(ctx context.Context, input *firehose.DescribeDeliveryStreamInput, optFns ...func(*firehose.Options)) (*firehose.DescribeDeliveryStreamOutput, error) {
		assert.Equal(t, "test", *input.DeliveryStreamName)
		return &firehose.DescribeDeliveryStreamOutput{
			DeliveryStreamDescription: &types.DeliveryStreamDescription{
				DeliveryStreamStatus: types.DeliveryStreamStatusCreating,
			},
		}, nil
	}
	f := &mock.FirehoseMock{DescribeDeliveryStreamFunc: describeFunc}
	writer := makeFirehoseWriterWithMock(f, "test")
	err := writer.validateStream(context.Background())
	assert.Error(t, err)
	assert.True(t, f.DescribeDeliveryStreamFuncInvoked)
}

func TestFirehoseValidateStreamError(t *testing.T) {
	describeFunc := func(ctx context.Context, input *firehose.DescribeDeliveryStreamInput, optFns ...func(*firehose.Options)) (*firehose.DescribeDeliveryStreamOutput, error) {
		assert.Equal(t, "test", *input.DeliveryStreamName)
		return nil, errors.New("boom!")
	}
	f := &mock.FirehoseMock{DescribeDeliveryStreamFunc: describeFunc}
	writer := makeFirehoseWriterWithMock(f, "test")
	err := writer.validateStream(context.Background())
	assert.Error(t, err)
	assert.True(t, f.DescribeDeliveryStreamFuncInvoked)
}
