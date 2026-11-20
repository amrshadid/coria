import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from coria.v1 import access_pb2 as _access_pb2
from coria.v1 import job_pb2 as _job_pb2
from coria.v1 import quarantine_pb2 as _quarantine_pb2
from coria.v1 import reception_pb2 as _reception_pb2
from coria.v1 import receiver_pb2 as _receiver_pb2
from coria.v1 import scope_pb2 as _scope_pb2
from coria.v1 import sender_pb2 as _sender_pb2
from coria.v1 import tenant_pb2 as _tenant_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class EventType(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    EVENT_TYPE_UNSPECIFIED: _ClassVar[EventType]
    EVENT_TYPE_ASSOCIATION_FAILED: _ClassVar[EventType]
    EVENT_TYPE_RECEPTION_STEP: _ClassVar[EventType]
    EVENT_TYPE_FILE_QUARANTINED: _ClassVar[EventType]
    EVENT_TYPE_QUARANTINE_DECIDED: _ClassVar[EventType]
    EVENT_TYPE_RECEIVER_HEALTH: _ClassVar[EventType]
    EVENT_TYPE_STORAGE_SPACE: _ClassVar[EventType]
    EVENT_TYPE_FRAMES_ADDED: _ClassVar[EventType]
    EVENT_TYPE_JOB_STEP: _ClassVar[EventType]
    EVENT_TYPE_JOB_PROGRESS: _ClassVar[EventType]
    EVENT_TYPE_SENDER_HEALTH: _ClassVar[EventType]

class AssociationFailure(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ASSOCIATION_FAILURE_UNSPECIFIED: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_CALLER_NOT_ALLOWED: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_CALLED_AE_TITLE_NOT_RECOGNIZED: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_NO_ACCEPTABLE_CONTEXT: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_NEARLY_FULL: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_TENANT_NOT_READY: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_TLS_FAILED: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_PROTOCOL_ERROR: _ClassVar[AssociationFailure]
    ASSOCIATION_FAILURE_INCOMING_LIMIT: _ClassVar[AssociationFailure]

class StorageSpaceState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STORAGE_SPACE_STATE_UNSPECIFIED: _ClassVar[StorageSpaceState]
    STORAGE_SPACE_STATE_NEARLY_FULL: _ClassVar[StorageSpaceState]
    STORAGE_SPACE_STATE_RECOVERED: _ClassVar[StorageSpaceState]
EVENT_TYPE_UNSPECIFIED: EventType
EVENT_TYPE_ASSOCIATION_FAILED: EventType
EVENT_TYPE_RECEPTION_STEP: EventType
EVENT_TYPE_FILE_QUARANTINED: EventType
EVENT_TYPE_QUARANTINE_DECIDED: EventType
EVENT_TYPE_RECEIVER_HEALTH: EventType
EVENT_TYPE_STORAGE_SPACE: EventType
EVENT_TYPE_FRAMES_ADDED: EventType
EVENT_TYPE_JOB_STEP: EventType
EVENT_TYPE_JOB_PROGRESS: EventType
EVENT_TYPE_SENDER_HEALTH: EventType
ASSOCIATION_FAILURE_UNSPECIFIED: AssociationFailure
ASSOCIATION_FAILURE_CALLER_NOT_ALLOWED: AssociationFailure
ASSOCIATION_FAILURE_CALLED_AE_TITLE_NOT_RECOGNIZED: AssociationFailure
ASSOCIATION_FAILURE_NO_ACCEPTABLE_CONTEXT: AssociationFailure
ASSOCIATION_FAILURE_NEARLY_FULL: AssociationFailure
ASSOCIATION_FAILURE_TENANT_NOT_READY: AssociationFailure
ASSOCIATION_FAILURE_TLS_FAILED: AssociationFailure
ASSOCIATION_FAILURE_PROTOCOL_ERROR: AssociationFailure
ASSOCIATION_FAILURE_INCOMING_LIMIT: AssociationFailure
STORAGE_SPACE_STATE_UNSPECIFIED: StorageSpaceState
STORAGE_SPACE_STATE_NEARLY_FULL: StorageSpaceState
STORAGE_SPACE_STATE_RECOVERED: StorageSpaceState

class ReceptionStepCount(_message.Message):
    __slots__ = ("step", "receptions")
    STEP_FIELD_NUMBER: _ClassVar[int]
    RECEPTIONS_FIELD_NUMBER: _ClassVar[int]
    step: _reception_pb2.ReceptionStep
    receptions: int
    def __init__(self, step: _Optional[_Union[_reception_pb2.ReceptionStep, str]] = ..., receptions: _Optional[int] = ...) -> None: ...

class TenantCounts(_message.Message):
    __slots__ = ("subjects", "sessions", "scans", "instances", "quarantined_files", "quarantined_bytes", "receptions_in_progress", "incoming_bytes", "jobs_in_progress")
    SUBJECTS_FIELD_NUMBER: _ClassVar[int]
    SESSIONS_FIELD_NUMBER: _ClassVar[int]
    SCANS_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_FIELD_NUMBER: _ClassVar[int]
    QUARANTINED_FILES_FIELD_NUMBER: _ClassVar[int]
    QUARANTINED_BYTES_FIELD_NUMBER: _ClassVar[int]
    RECEPTIONS_IN_PROGRESS_FIELD_NUMBER: _ClassVar[int]
    INCOMING_BYTES_FIELD_NUMBER: _ClassVar[int]
    JOBS_IN_PROGRESS_FIELD_NUMBER: _ClassVar[int]
    subjects: int
    sessions: int
    scans: int
    instances: int
    quarantined_files: int
    quarantined_bytes: int
    receptions_in_progress: _containers.RepeatedCompositeFieldContainer[ReceptionStepCount]
    incoming_bytes: int
    jobs_in_progress: _containers.RepeatedCompositeFieldContainer[JobStateCount]
    def __init__(self, subjects: _Optional[int] = ..., sessions: _Optional[int] = ..., scans: _Optional[int] = ..., instances: _Optional[int] = ..., quarantined_files: _Optional[int] = ..., quarantined_bytes: _Optional[int] = ..., receptions_in_progress: _Optional[_Iterable[_Union[ReceptionStepCount, _Mapping]]] = ..., incoming_bytes: _Optional[int] = ..., jobs_in_progress: _Optional[_Iterable[_Union[JobStateCount, _Mapping]]] = ...) -> None: ...

class JobStateCount(_message.Message):
    __slots__ = ("state", "jobs")
    STATE_FIELD_NUMBER: _ClassVar[int]
    JOBS_FIELD_NUMBER: _ClassVar[int]
    state: _job_pb2.JobState
    jobs: int
    def __init__(self, state: _Optional[_Union[_job_pb2.JobState, str]] = ..., jobs: _Optional[int] = ...) -> None: ...

class SenderStatus(_message.Message):
    __slots__ = ("sender", "health", "open_associations")
    SENDER_FIELD_NUMBER: _ClassVar[int]
    HEALTH_FIELD_NUMBER: _ClassVar[int]
    OPEN_ASSOCIATIONS_FIELD_NUMBER: _ClassVar[int]
    sender: str
    health: _sender_pb2.SenderHealth
    open_associations: int
    def __init__(self, sender: _Optional[str] = ..., health: _Optional[_Union[_sender_pb2.SenderHealth, _Mapping]] = ..., open_associations: _Optional[int] = ...) -> None: ...

class Throughput(_message.Message):
    __slots__ = ("files_received_per_second", "bytes_received_per_second", "files_archived_per_second", "bytes_archived_per_second")
    FILES_RECEIVED_PER_SECOND_FIELD_NUMBER: _ClassVar[int]
    BYTES_RECEIVED_PER_SECOND_FIELD_NUMBER: _ClassVar[int]
    FILES_ARCHIVED_PER_SECOND_FIELD_NUMBER: _ClassVar[int]
    BYTES_ARCHIVED_PER_SECOND_FIELD_NUMBER: _ClassVar[int]
    files_received_per_second: float
    bytes_received_per_second: float
    files_archived_per_second: float
    bytes_archived_per_second: float
    def __init__(self, files_received_per_second: _Optional[float] = ..., bytes_received_per_second: _Optional[float] = ..., files_archived_per_second: _Optional[float] = ..., bytes_archived_per_second: _Optional[float] = ...) -> None: ...

class StorageStatus(_message.Message):
    __slots__ = ("storage", "used_bytes", "free_bytes", "capacity_bytes", "nearly_full")
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    USED_BYTES_FIELD_NUMBER: _ClassVar[int]
    FREE_BYTES_FIELD_NUMBER: _ClassVar[int]
    CAPACITY_BYTES_FIELD_NUMBER: _ClassVar[int]
    NEARLY_FULL_FIELD_NUMBER: _ClassVar[int]
    storage: str
    used_bytes: int
    free_bytes: int
    capacity_bytes: int
    nearly_full: bool
    def __init__(self, storage: _Optional[str] = ..., used_bytes: _Optional[int] = ..., free_bytes: _Optional[int] = ..., capacity_bytes: _Optional[int] = ..., nearly_full: _Optional[bool] = ...) -> None: ...

class ReceiverStatus(_message.Message):
    __slots__ = ("receiver", "health", "any_caller")
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    HEALTH_FIELD_NUMBER: _ClassVar[int]
    ANY_CALLER_FIELD_NUMBER: _ClassVar[int]
    receiver: str
    health: _receiver_pb2.ReceiverHealth
    any_caller: bool
    def __init__(self, receiver: _Optional[str] = ..., health: _Optional[_Union[_receiver_pb2.ReceiverHealth, _Mapping]] = ..., any_caller: _Optional[bool] = ...) -> None: ...

class TenantStatus(_message.Message):
    __slots__ = ("tenant", "database_state", "database_state_time", "event_cursor", "counts", "throughput", "storages", "receivers", "incoming_limit_bytes", "senders")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    DATABASE_STATE_FIELD_NUMBER: _ClassVar[int]
    DATABASE_STATE_TIME_FIELD_NUMBER: _ClassVar[int]
    EVENT_CURSOR_FIELD_NUMBER: _ClassVar[int]
    COUNTS_FIELD_NUMBER: _ClassVar[int]
    THROUGHPUT_FIELD_NUMBER: _ClassVar[int]
    STORAGES_FIELD_NUMBER: _ClassVar[int]
    RECEIVERS_FIELD_NUMBER: _ClassVar[int]
    INCOMING_LIMIT_BYTES_FIELD_NUMBER: _ClassVar[int]
    SENDERS_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    database_state: _tenant_pb2.TenantState
    database_state_time: _timestamp_pb2.Timestamp
    event_cursor: str
    counts: TenantCounts
    throughput: Throughput
    storages: _containers.RepeatedCompositeFieldContainer[StorageStatus]
    receivers: _containers.RepeatedCompositeFieldContainer[ReceiverStatus]
    incoming_limit_bytes: int
    senders: _containers.RepeatedCompositeFieldContainer[SenderStatus]
    def __init__(self, tenant: _Optional[str] = ..., database_state: _Optional[_Union[_tenant_pb2.TenantState, str]] = ..., database_state_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., event_cursor: _Optional[str] = ..., counts: _Optional[_Union[TenantCounts, _Mapping]] = ..., throughput: _Optional[_Union[Throughput, _Mapping]] = ..., storages: _Optional[_Iterable[_Union[StorageStatus, _Mapping]]] = ..., receivers: _Optional[_Iterable[_Union[ReceiverStatus, _Mapping]]] = ..., incoming_limit_bytes: _Optional[int] = ..., senders: _Optional[_Iterable[_Union[SenderStatus, _Mapping]]] = ...) -> None: ...

class GetStatusRequest(_message.Message):
    __slots__ = ("scope", "page_size", "page_token")
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    scope: _scope_pb2.Scope
    page_size: int
    page_token: str
    def __init__(self, scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class GetStatusResponse(_message.Message):
    __slots__ = ("tenants", "next_page_token", "time", "expiring_keys")
    TENANTS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    TIME_FIELD_NUMBER: _ClassVar[int]
    EXPIRING_KEYS_FIELD_NUMBER: _ClassVar[int]
    tenants: _containers.RepeatedCompositeFieldContainer[TenantStatus]
    next_page_token: str
    time: _timestamp_pb2.Timestamp
    expiring_keys: _containers.RepeatedCompositeFieldContainer[_access_pb2.Key]
    def __init__(self, tenants: _Optional[_Iterable[_Union[TenantStatus, _Mapping]]] = ..., next_page_token: _Optional[str] = ..., time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., expiring_keys: _Optional[_Iterable[_Union[_access_pb2.Key, _Mapping]]] = ...) -> None: ...

class AssociationFailed(_message.Message):
    __slots__ = ("receiver", "caller", "failure", "count", "hosts")
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    CALLER_FIELD_NUMBER: _ClassVar[int]
    FAILURE_FIELD_NUMBER: _ClassVar[int]
    COUNT_FIELD_NUMBER: _ClassVar[int]
    HOSTS_FIELD_NUMBER: _ClassVar[int]
    receiver: str
    caller: _quarantine_pb2.Caller
    failure: AssociationFailure
    count: int
    hosts: int
    def __init__(self, receiver: _Optional[str] = ..., caller: _Optional[_Union[_quarantine_pb2.Caller, _Mapping]] = ..., failure: _Optional[_Union[AssociationFailure, str]] = ..., count: _Optional[int] = ..., hosts: _Optional[int] = ...) -> None: ...

class ReceptionStepReached(_message.Message):
    __slots__ = ("reception", "receiver", "arrival", "step", "previous_step", "counts", "subject_label", "session_label", "retry_of")
    RECEPTION_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    ARRIVAL_FIELD_NUMBER: _ClassVar[int]
    STEP_FIELD_NUMBER: _ClassVar[int]
    PREVIOUS_STEP_FIELD_NUMBER: _ClassVar[int]
    COUNTS_FIELD_NUMBER: _ClassVar[int]
    SUBJECT_LABEL_FIELD_NUMBER: _ClassVar[int]
    SESSION_LABEL_FIELD_NUMBER: _ClassVar[int]
    RETRY_OF_FIELD_NUMBER: _ClassVar[int]
    reception: str
    receiver: str
    arrival: int
    step: _reception_pb2.ReceptionStep
    previous_step: _reception_pb2.ReceptionStep
    counts: _reception_pb2.ReceptionCounts
    subject_label: str
    session_label: str
    retry_of: str
    def __init__(self, reception: _Optional[str] = ..., receiver: _Optional[str] = ..., arrival: _Optional[int] = ..., step: _Optional[_Union[_reception_pb2.ReceptionStep, str]] = ..., previous_step: _Optional[_Union[_reception_pb2.ReceptionStep, str]] = ..., counts: _Optional[_Union[_reception_pb2.ReceptionCounts, _Mapping]] = ..., subject_label: _Optional[str] = ..., session_label: _Optional[str] = ..., retry_of: _Optional[str] = ...) -> None: ...

class FileQuarantined(_message.Message):
    __slots__ = ("file", "reception", "receiver", "reason", "size_bytes", "held_files", "held_bytes")
    FILE_FIELD_NUMBER: _ClassVar[int]
    RECEPTION_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    SIZE_BYTES_FIELD_NUMBER: _ClassVar[int]
    HELD_FILES_FIELD_NUMBER: _ClassVar[int]
    HELD_BYTES_FIELD_NUMBER: _ClassVar[int]
    file: str
    reception: str
    receiver: str
    reason: _quarantine_pb2.QuarantineReason
    size_bytes: int
    held_files: int
    held_bytes: int
    def __init__(self, file: _Optional[str] = ..., reception: _Optional[str] = ..., receiver: _Optional[str] = ..., reason: _Optional[_Union[_quarantine_pb2.QuarantineReason, str]] = ..., size_bytes: _Optional[int] = ..., held_files: _Optional[int] = ..., held_bytes: _Optional[int] = ...) -> None: ...

class QuarantineDecided(_message.Message):
    __slots__ = ("file", "state", "release_mode", "release_reception", "held_files", "held_bytes")
    FILE_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    RELEASE_MODE_FIELD_NUMBER: _ClassVar[int]
    RELEASE_RECEPTION_FIELD_NUMBER: _ClassVar[int]
    HELD_FILES_FIELD_NUMBER: _ClassVar[int]
    HELD_BYTES_FIELD_NUMBER: _ClassVar[int]
    file: str
    state: _quarantine_pb2.QuarantineState
    release_mode: _quarantine_pb2.ReleaseMode
    release_reception: str
    held_files: int
    held_bytes: int
    def __init__(self, file: _Optional[str] = ..., state: _Optional[_Union[_quarantine_pb2.QuarantineState, str]] = ..., release_mode: _Optional[_Union[_quarantine_pb2.ReleaseMode, str]] = ..., release_reception: _Optional[str] = ..., held_files: _Optional[int] = ..., held_bytes: _Optional[int] = ...) -> None: ...

class ReceiverHealthChanged(_message.Message):
    __slots__ = ("receiver", "health")
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    HEALTH_FIELD_NUMBER: _ClassVar[int]
    receiver: str
    health: _receiver_pb2.ReceiverHealth
    def __init__(self, receiver: _Optional[str] = ..., health: _Optional[_Union[_receiver_pb2.ReceiverHealth, _Mapping]] = ...) -> None: ...

class StorageSpaceChanged(_message.Message):
    __slots__ = ("storage", "state", "used_bytes", "free_bytes")
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    USED_BYTES_FIELD_NUMBER: _ClassVar[int]
    FREE_BYTES_FIELD_NUMBER: _ClassVar[int]
    storage: str
    state: StorageSpaceState
    used_bytes: int
    free_bytes: int
    def __init__(self, storage: _Optional[str] = ..., state: _Optional[_Union[StorageSpaceState, str]] = ..., used_bytes: _Optional[int] = ..., free_bytes: _Optional[int] = ...) -> None: ...

class FramesAdded(_message.Message):
    __slots__ = ("subject_label", "session_label", "scan_label", "instances", "scans_done", "instances_done", "finished")
    SUBJECT_LABEL_FIELD_NUMBER: _ClassVar[int]
    SESSION_LABEL_FIELD_NUMBER: _ClassVar[int]
    SCAN_LABEL_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_FIELD_NUMBER: _ClassVar[int]
    SCANS_DONE_FIELD_NUMBER: _ClassVar[int]
    INSTANCES_DONE_FIELD_NUMBER: _ClassVar[int]
    FINISHED_FIELD_NUMBER: _ClassVar[int]
    subject_label: str
    session_label: str
    scan_label: str
    instances: int
    scans_done: int
    instances_done: int
    finished: bool
    def __init__(self, subject_label: _Optional[str] = ..., session_label: _Optional[str] = ..., scan_label: _Optional[str] = ..., instances: _Optional[int] = ..., scans_done: _Optional[int] = ..., instances_done: _Optional[int] = ..., finished: _Optional[bool] = ...) -> None: ...

class JobStepReached(_message.Message):
    __slots__ = ("job", "state", "previous_state", "counts", "sender", "receiver", "reception", "retry_of", "reason")
    JOB_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    PREVIOUS_STATE_FIELD_NUMBER: _ClassVar[int]
    COUNTS_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    RECEPTION_FIELD_NUMBER: _ClassVar[int]
    RETRY_OF_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    job: str
    state: _job_pb2.JobState
    previous_state: _job_pb2.JobState
    counts: _job_pb2.JobCounts
    sender: str
    receiver: str
    reception: str
    retry_of: str
    reason: str
    def __init__(self, job: _Optional[str] = ..., state: _Optional[_Union[_job_pb2.JobState, str]] = ..., previous_state: _Optional[_Union[_job_pb2.JobState, str]] = ..., counts: _Optional[_Union[_job_pb2.JobCounts, _Mapping]] = ..., sender: _Optional[str] = ..., receiver: _Optional[str] = ..., reception: _Optional[str] = ..., retry_of: _Optional[str] = ..., reason: _Optional[str] = ...) -> None: ...

class JobProgressed(_message.Message):
    __slots__ = ("job", "counts", "sender")
    JOB_FIELD_NUMBER: _ClassVar[int]
    COUNTS_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    job: str
    counts: _job_pb2.JobCounts
    sender: str
    def __init__(self, job: _Optional[str] = ..., counts: _Optional[_Union[_job_pb2.JobCounts, _Mapping]] = ..., sender: _Optional[str] = ...) -> None: ...

class SenderHealthChanged(_message.Message):
    __slots__ = ("sender", "health")
    SENDER_FIELD_NUMBER: _ClassVar[int]
    HEALTH_FIELD_NUMBER: _ClassVar[int]
    sender: str
    health: _sender_pb2.SenderHealth
    def __init__(self, sender: _Optional[str] = ..., health: _Optional[_Union[_sender_pb2.SenderHealth, _Mapping]] = ...) -> None: ...

class Event(_message.Message):
    __slots__ = ("cursor", "time", "tenant", "type", "association_failed", "reception_step", "file_quarantined", "quarantine_decided", "receiver_health", "storage_space", "frames_added", "job_step", "job_progress", "sender_health")
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    TIME_FIELD_NUMBER: _ClassVar[int]
    TENANT_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    ASSOCIATION_FAILED_FIELD_NUMBER: _ClassVar[int]
    RECEPTION_STEP_FIELD_NUMBER: _ClassVar[int]
    FILE_QUARANTINED_FIELD_NUMBER: _ClassVar[int]
    QUARANTINE_DECIDED_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_HEALTH_FIELD_NUMBER: _ClassVar[int]
    STORAGE_SPACE_FIELD_NUMBER: _ClassVar[int]
    FRAMES_ADDED_FIELD_NUMBER: _ClassVar[int]
    JOB_STEP_FIELD_NUMBER: _ClassVar[int]
    JOB_PROGRESS_FIELD_NUMBER: _ClassVar[int]
    SENDER_HEALTH_FIELD_NUMBER: _ClassVar[int]
    cursor: str
    time: _timestamp_pb2.Timestamp
    tenant: str
    type: EventType
    association_failed: AssociationFailed
    reception_step: ReceptionStepReached
    file_quarantined: FileQuarantined
    quarantine_decided: QuarantineDecided
    receiver_health: ReceiverHealthChanged
    storage_space: StorageSpaceChanged
    frames_added: FramesAdded
    job_step: JobStepReached
    job_progress: JobProgressed
    sender_health: SenderHealthChanged
    def __init__(self, cursor: _Optional[str] = ..., time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., tenant: _Optional[str] = ..., type: _Optional[_Union[EventType, str]] = ..., association_failed: _Optional[_Union[AssociationFailed, _Mapping]] = ..., reception_step: _Optional[_Union[ReceptionStepReached, _Mapping]] = ..., file_quarantined: _Optional[_Union[FileQuarantined, _Mapping]] = ..., quarantine_decided: _Optional[_Union[QuarantineDecided, _Mapping]] = ..., receiver_health: _Optional[_Union[ReceiverHealthChanged, _Mapping]] = ..., storage_space: _Optional[_Union[StorageSpaceChanged, _Mapping]] = ..., frames_added: _Optional[_Union[FramesAdded, _Mapping]] = ..., job_step: _Optional[_Union[JobStepReached, _Mapping]] = ..., job_progress: _Optional[_Union[JobProgressed, _Mapping]] = ..., sender_health: _Optional[_Union[SenderHealthChanged, _Mapping]] = ...) -> None: ...

class WatchKeepAlive(_message.Message):
    __slots__ = ("time", "cursor", "tenant_cursors", "unanswered_tenants", "full")
    class TenantCursorsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    TIME_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    TENANT_CURSORS_FIELD_NUMBER: _ClassVar[int]
    UNANSWERED_TENANTS_FIELD_NUMBER: _ClassVar[int]
    FULL_FIELD_NUMBER: _ClassVar[int]
    time: _timestamp_pb2.Timestamp
    cursor: str
    tenant_cursors: _containers.ScalarMap[str, str]
    unanswered_tenants: _containers.RepeatedScalarFieldContainer[str]
    full: bool
    def __init__(self, time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., cursor: _Optional[str] = ..., tenant_cursors: _Optional[_Mapping[str, str]] = ..., unanswered_tenants: _Optional[_Iterable[str]] = ..., full: _Optional[bool] = ...) -> None: ...

class WatchRequest(_message.Message):
    __slots__ = ("scope", "cursor", "types", "tenant_cursors", "resume")
    class TenantCursorsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    TYPES_FIELD_NUMBER: _ClassVar[int]
    TENANT_CURSORS_FIELD_NUMBER: _ClassVar[int]
    RESUME_FIELD_NUMBER: _ClassVar[int]
    scope: _scope_pb2.Scope
    cursor: str
    types: _containers.RepeatedScalarFieldContainer[EventType]
    tenant_cursors: _containers.ScalarMap[str, str]
    resume: bool
    def __init__(self, scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ..., cursor: _Optional[str] = ..., types: _Optional[_Iterable[_Union[EventType, str]]] = ..., tenant_cursors: _Optional[_Mapping[str, str]] = ..., resume: _Optional[bool] = ...) -> None: ...

class WatchResponse(_message.Message):
    __slots__ = ("event", "keep_alive")
    EVENT_FIELD_NUMBER: _ClassVar[int]
    KEEP_ALIVE_FIELD_NUMBER: _ClassVar[int]
    event: Event
    keep_alive: WatchKeepAlive
    def __init__(self, event: _Optional[_Union[Event, _Mapping]] = ..., keep_alive: _Optional[_Union[WatchKeepAlive, _Mapping]] = ...) -> None: ...
