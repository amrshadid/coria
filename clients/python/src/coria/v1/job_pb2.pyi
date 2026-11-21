import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class JobState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    JOB_STATE_UNSPECIFIED: _ClassVar[JobState]
    JOB_STATE_QUEUED: _ClassVar[JobState]
    JOB_STATE_RUNNING: _ClassVar[JobState]
    JOB_STATE_DONE: _ClassVar[JobState]
    JOB_STATE_PARTIAL: _ClassVar[JobState]
    JOB_STATE_FAILED: _ClassVar[JobState]
    JOB_STATE_CANCELED: _ClassVar[JobState]

class DeidJobMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    DEID_JOB_MODE_UNSPECIFIED: _ClassVar[DeidJobMode]
    DEID_JOB_MODE_DRY_RUN: _ClassVar[DeidJobMode]
    DEID_JOB_MODE_CHECK: _ClassVar[DeidJobMode]

class RebuildJobMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REBUILD_JOB_MODE_UNSPECIFIED: _ClassVar[RebuildJobMode]
    REBUILD_JOB_MODE_COMPARE: _ClassVar[RebuildJobMode]
    REBUILD_JOB_MODE_REPLACE: _ClassVar[RebuildJobMode]

class JobFailureCode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    JOB_FAILURE_CODE_UNSPECIFIED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_PEER_REFUSED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_PEER_WARNING: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_CLASS_NOT_ACCEPTED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_SYNTAX_NOT_ACCEPTED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_PEER_UNACCOUNTED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_DAMAGED_IN_STORAGE: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_META_UNREADABLE: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_WOULD_HOLD: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_CHECK_FAILED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_UNDECIDED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_NOT_DEIDENTIFIED: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_TOO_LARGE: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_DELETED_IN_ARCHIVE: _ClassVar[JobFailureCode]
    JOB_FAILURE_CODE_NOT_ONLINE: _ClassVar[JobFailureCode]
JOB_STATE_UNSPECIFIED: JobState
JOB_STATE_QUEUED: JobState
JOB_STATE_RUNNING: JobState
JOB_STATE_DONE: JobState
JOB_STATE_PARTIAL: JobState
JOB_STATE_FAILED: JobState
JOB_STATE_CANCELED: JobState
DEID_JOB_MODE_UNSPECIFIED: DeidJobMode
DEID_JOB_MODE_DRY_RUN: DeidJobMode
DEID_JOB_MODE_CHECK: DeidJobMode
REBUILD_JOB_MODE_UNSPECIFIED: RebuildJobMode
REBUILD_JOB_MODE_COMPARE: RebuildJobMode
REBUILD_JOB_MODE_REPLACE: RebuildJobMode
JOB_FAILURE_CODE_UNSPECIFIED: JobFailureCode
JOB_FAILURE_CODE_PEER_REFUSED: JobFailureCode
JOB_FAILURE_CODE_PEER_WARNING: JobFailureCode
JOB_FAILURE_CODE_CLASS_NOT_ACCEPTED: JobFailureCode
JOB_FAILURE_CODE_SYNTAX_NOT_ACCEPTED: JobFailureCode
JOB_FAILURE_CODE_PEER_UNACCOUNTED: JobFailureCode
JOB_FAILURE_CODE_DAMAGED_IN_STORAGE: JobFailureCode
JOB_FAILURE_CODE_META_UNREADABLE: JobFailureCode
JOB_FAILURE_CODE_WOULD_HOLD: JobFailureCode
JOB_FAILURE_CODE_CHECK_FAILED: JobFailureCode
JOB_FAILURE_CODE_UNDECIDED: JobFailureCode
JOB_FAILURE_CODE_NOT_DEIDENTIFIED: JobFailureCode
JOB_FAILURE_CODE_TOO_LARGE: JobFailureCode
JOB_FAILURE_CODE_DELETED_IN_ARCHIVE: JobFailureCode
JOB_FAILURE_CODE_NOT_ONLINE: JobFailureCode

class JobCounts(_message.Message):
    __slots__ = ("instances", "bytes", "instances_sent", "bytes_sent", "instances_warned", "bytes_warned", "instances_refused", "bytes_refused", "instances_remaining", "bytes_remaining", "scans", "scans_done", "listed")
    INSTANCES_FIELD_NUMBER: _ClassVar[int]
    BYTES_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_SENT_FIELD_NUMBER: _ClassVar[int]
    BYTES_SENT_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_WARNED_FIELD_NUMBER: _ClassVar[int]
    BYTES_WARNED_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_REFUSED_FIELD_NUMBER: _ClassVar[int]
    BYTES_REFUSED_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_REMAINING_FIELD_NUMBER: _ClassVar[int]
    BYTES_REMAINING_FIELD_NUMBER: _ClassVar[int]
    SCANS_FIELD_NUMBER: _ClassVar[int]
    SCANS_DONE_FIELD_NUMBER: _ClassVar[int]
    LISTED_FIELD_NUMBER: _ClassVar[int]
    instances: int
    bytes: int
    instances_sent: int
    bytes_sent: int
    instances_warned: int
    bytes_warned: int
    instances_refused: int
    bytes_refused: int
    instances_remaining: int
    bytes_remaining: int
    scans: int
    scans_done: int
    listed: bool
    def __init__(self, instances: _Optional[int] = ..., bytes: _Optional[int] = ..., instances_sent: _Optional[int] = ..., bytes_sent: _Optional[int] = ..., instances_warned: _Optional[int] = ..., bytes_warned: _Optional[int] = ..., instances_refused: _Optional[int] = ..., bytes_refused: _Optional[int] = ..., instances_remaining: _Optional[int] = ..., bytes_remaining: _Optional[int] = ..., scans: _Optional[int] = ..., scans_done: _Optional[int] = ..., listed: _Optional[bool] = ...) -> None: ...

class SendJob(_message.Message):
    __slots__ = ("sender", "subject_label", "session_label", "scan_label", "reception", "receiver")
    SENDER_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_LABEL_FIELD_NUMBER: _ClassVar[int]
    SESSION_LABEL_FIELD_NUMBER: _ClassVar[int]
    SCAN_LABEL_FIELD_NUMBER: _ClassVar[int]
    RECEPTION_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    sender: str
    subject_label: str
    session_label: str
    scan_label: str
    reception: str
    receiver: str
    def __init__(self, sender: _Optional[str] = ..., subject_label: _Optional[str] = ..., session_label: _Optional[str] = ..., scan_label: _Optional[str] = ..., reception: _Optional[str] = ..., receiver: _Optional[str] = ...) -> None: ...

class DeidJob(_message.Message):
    __slots__ = ("mode", "profile", "profile_version", "subject_label", "session_label", "scan_label")
    MODE_FIELD_NUMBER: _ClassVar[int]
    PROFILE_FIELD_NUMBER: _ClassVar[int]
    PROFILE_VERSION_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_LABEL_FIELD_NUMBER: _ClassVar[int]
    SESSION_LABEL_FIELD_NUMBER: _ClassVar[int]
    SCAN_LABEL_FIELD_NUMBER: _ClassVar[int]
    mode: DeidJobMode
    profile: str
    profile_version: int
    subject_label: str
    session_label: str
    scan_label: str
    def __init__(self, mode: _Optional[_Union[DeidJobMode, str]] = ..., profile: _Optional[str] = ..., profile_version: _Optional[int] = ..., subject_label: _Optional[str] = ..., session_label: _Optional[str] = ..., scan_label: _Optional[str] = ...) -> None: ...

class ScrubJob(_message.Message):
    __slots__ = ("storage", "scheduled")
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    SCHEDULED_FIELD_NUMBER: _ClassVar[int]
    storage: str
    scheduled: bool
    def __init__(self, storage: _Optional[str] = ..., scheduled: _Optional[bool] = ...) -> None: ...

class RebuildJob(_message.Message):
    __slots__ = ("mode", "scratch_schema")
    MODE_FIELD_NUMBER: _ClassVar[int]
    SCRATCH_SCHEMA_FIELD_NUMBER: _ClassVar[int]
    mode: RebuildJobMode
    scratch_schema: str
    def __init__(self, mode: _Optional[_Union[RebuildJobMode, str]] = ..., scratch_schema: _Optional[str] = ...) -> None: ...

class JobTally(_message.Message):
    __slots__ = ("key", "count")
    KEY_FIELD_NUMBER: _ClassVar[int]
    COUNT_FIELD_NUMBER: _ClassVar[int]
    key: str
    count: int
    def __init__(self, key: _Optional[str] = ..., count: _Optional[int] = ...) -> None: ...

class Job(_message.Message):
    __slots__ = ("id", "send", "deid", "scrub", "rebuild", "state", "counts", "reason", "created_by_key_id", "create_time", "start_time", "state_time", "end_time", "retry_of", "retry_job", "failures_not_listed", "tallies")
    ID_FIELD_NUMBER: _ClassVar[int]
    SEND_FIELD_NUMBER: _ClassVar[int]
    DEID_FIELD_NUMBER: _ClassVar[int]
    SCRUB_FIELD_NUMBER: _ClassVar[int]
    REBUILD_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    COUNTS_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_KEY_ID_FIELD_NUMBER: _ClassVar[int]
    CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    START_TIME_FIELD_NUMBER: _ClassVar[int]
    STATE_TIME_FIELD_NUMBER: _ClassVar[int]
    END_TIME_FIELD_NUMBER: _ClassVar[int]
    RETRY_OF_FIELD_NUMBER: _ClassVar[int]
    RETRY_JOB_FIELD_NUMBER: _ClassVar[int]
    FAILURES_NOT_LISTED_FIELD_NUMBER: _ClassVar[int]
    TALLIES_FIELD_NUMBER: _ClassVar[int]
    id: str
    send: SendJob
    deid: DeidJob
    scrub: ScrubJob
    rebuild: RebuildJob
    state: JobState
    counts: JobCounts
    reason: str
    created_by_key_id: str
    create_time: _timestamp_pb2.Timestamp
    start_time: _timestamp_pb2.Timestamp
    state_time: _timestamp_pb2.Timestamp
    end_time: _timestamp_pb2.Timestamp
    retry_of: str
    retry_job: str
    failures_not_listed: int
    tallies: _containers.RepeatedCompositeFieldContainer[JobTally]
    def __init__(self, id: _Optional[str] = ..., send: _Optional[_Union[SendJob, _Mapping]] = ..., deid: _Optional[_Union[DeidJob, _Mapping]] = ..., scrub: _Optional[_Union[ScrubJob, _Mapping]] = ..., rebuild: _Optional[_Union[RebuildJob, _Mapping]] = ..., state: _Optional[_Union[JobState, str]] = ..., counts: _Optional[_Union[JobCounts, _Mapping]] = ..., reason: _Optional[str] = ..., created_by_key_id: _Optional[str] = ..., create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., start_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., state_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., end_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., retry_of: _Optional[str] = ..., retry_job: _Optional[str] = ..., failures_not_listed: _Optional[int] = ..., tallies: _Optional[_Iterable[_Union[JobTally, _Mapping]]] = ...) -> None: ...

class JobFailure(_message.Message):
    __slots__ = ("instance_id", "subject_label", "session_label", "scan_label", "code", "peer_status", "sop_class_uid", "transfer_syntax_uid", "detail", "time", "peer_status_http", "revision")
    INSTANCE_ID_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_LABEL_FIELD_NUMBER: _ClassVar[int]
    SESSION_LABEL_FIELD_NUMBER: _ClassVar[int]
    SCAN_LABEL_FIELD_NUMBER: _ClassVar[int]
    CODE_FIELD_NUMBER: _ClassVar[int]
    PEER_STATUS_FIELD_NUMBER: _ClassVar[int]
    SOP_CLASS_UID_FIELD_NUMBER: _ClassVar[int]
    TRANSFER_SYNTAX_UID_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    TIME_FIELD_NUMBER: _ClassVar[int]
    PEER_STATUS_HTTP_FIELD_NUMBER: _ClassVar[int]
    REVISION_FIELD_NUMBER: _ClassVar[int]
    instance_id: str
    subject_label: str
    session_label: str
    scan_label: str
    code: JobFailureCode
    peer_status: int
    sop_class_uid: str
    transfer_syntax_uid: str
    detail: str
    time: _timestamp_pb2.Timestamp
    peer_status_http: bool
    revision: int
    def __init__(self, instance_id: _Optional[str] = ..., subject_label: _Optional[str] = ..., session_label: _Optional[str] = ..., scan_label: _Optional[str] = ..., code: _Optional[_Union[JobFailureCode, str]] = ..., peer_status: _Optional[int] = ..., sop_class_uid: _Optional[str] = ..., transfer_syntax_uid: _Optional[str] = ..., detail: _Optional[str] = ..., time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., peer_status_http: _Optional[bool] = ..., revision: _Optional[int] = ...) -> None: ...

class CreateJobRequest(_message.Message):
    __slots__ = ("tenant", "send", "deid", "scrub", "rebuild")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    SEND_FIELD_NUMBER: _ClassVar[int]
    DEID_FIELD_NUMBER: _ClassVar[int]
    SCRUB_FIELD_NUMBER: _ClassVar[int]
    REBUILD_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    send: SendJob
    deid: DeidJob
    scrub: ScrubJob
    rebuild: RebuildJob
    def __init__(self, tenant: _Optional[str] = ..., send: _Optional[_Union[SendJob, _Mapping]] = ..., deid: _Optional[_Union[DeidJob, _Mapping]] = ..., scrub: _Optional[_Union[ScrubJob, _Mapping]] = ..., rebuild: _Optional[_Union[RebuildJob, _Mapping]] = ...) -> None: ...

class CreateJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class GetJobRequest(_message.Message):
    __slots__ = ("tenant", "id")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    ID_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    id: str
    def __init__(self, tenant: _Optional[str] = ..., id: _Optional[str] = ...) -> None: ...

class GetJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class ListJobsRequest(_message.Message):
    __slots__ = ("tenant", "states", "in_progress", "sender", "start_time", "end_time", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    STATES_FIELD_NUMBER: _ClassVar[int]
    IN_PROGRESS_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    START_TIME_FIELD_NUMBER: _ClassVar[int]
    END_TIME_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    states: _containers.RepeatedScalarFieldContainer[JobState]
    in_progress: bool
    sender: str
    start_time: _timestamp_pb2.Timestamp
    end_time: _timestamp_pb2.Timestamp
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., states: _Optional[_Iterable[_Union[JobState, str]]] = ..., in_progress: _Optional[bool] = ..., sender: _Optional[str] = ..., start_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., end_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListJobsResponse(_message.Message):
    __slots__ = ("jobs", "next_page_token")
    JOBS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    jobs: _containers.RepeatedCompositeFieldContainer[Job]
    next_page_token: str
    def __init__(self, jobs: _Optional[_Iterable[_Union[Job, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class CancelJobRequest(_message.Message):
    __slots__ = ("tenant", "id")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    ID_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    id: str
    def __init__(self, tenant: _Optional[str] = ..., id: _Optional[str] = ...) -> None: ...

class CancelJobResponse(_message.Message):
    __slots__ = ("job",)
    JOB_FIELD_NUMBER: _ClassVar[int]
    job: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class RetryJobRequest(_message.Message):
    __slots__ = ("tenant", "id")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    ID_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    id: str
    def __init__(self, tenant: _Optional[str] = ..., id: _Optional[str] = ...) -> None: ...

class RetryJobResponse(_message.Message):
    __slots__ = ("job", "retry")
    JOB_FIELD_NUMBER: _ClassVar[int]
    RETRY_FIELD_NUMBER: _ClassVar[int]
    job: Job
    retry: Job
    def __init__(self, job: _Optional[_Union[Job, _Mapping]] = ..., retry: _Optional[_Union[Job, _Mapping]] = ...) -> None: ...

class ListJobFailuresRequest(_message.Message):
    __slots__ = ("tenant", "id", "include_warnings", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    ID_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_WARNINGS_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    id: str
    include_warnings: bool
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., id: _Optional[str] = ..., include_warnings: _Optional[bool] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListJobFailuresResponse(_message.Message):
    __slots__ = ("failures", "next_page_token")
    FAILURES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    failures: _containers.RepeatedCompositeFieldContainer[JobFailure]
    next_page_token: str
    def __init__(self, failures: _Optional[_Iterable[_Union[JobFailure, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...
