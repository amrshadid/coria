import datetime

from google.protobuf import duration_pb2 as _duration_pb2
from google.protobuf import field_mask_pb2 as _field_mask_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ReceiverState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RECEIVER_STATE_UNSPECIFIED: _ClassVar[ReceiverState]
    RECEIVER_STATE_STARTING: _ClassVar[ReceiverState]
    RECEIVER_STATE_UP: _ClassVar[ReceiverState]
    RECEIVER_STATE_DOWN: _ClassVar[ReceiverState]

class ReceiverDownReason(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RECEIVER_DOWN_REASON_UNSPECIFIED: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_DISABLED: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_ADDRESS_IN_USE: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_PERMISSION_DENIED: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_ADDRESS_NOT_AVAILABLE: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_TLS: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_NEARLY_FULL: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_TENANT_NOT_READY: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_STOPPING: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_OTHER: _ClassVar[ReceiverDownReason]
    RECEIVER_DOWN_REASON_INCOMING_LIMIT: _ClassVar[ReceiverDownReason]
RECEIVER_STATE_UNSPECIFIED: ReceiverState
RECEIVER_STATE_STARTING: ReceiverState
RECEIVER_STATE_UP: ReceiverState
RECEIVER_STATE_DOWN: ReceiverState
RECEIVER_DOWN_REASON_UNSPECIFIED: ReceiverDownReason
RECEIVER_DOWN_REASON_DISABLED: ReceiverDownReason
RECEIVER_DOWN_REASON_ADDRESS_IN_USE: ReceiverDownReason
RECEIVER_DOWN_REASON_PERMISSION_DENIED: ReceiverDownReason
RECEIVER_DOWN_REASON_ADDRESS_NOT_AVAILABLE: ReceiverDownReason
RECEIVER_DOWN_REASON_TLS: ReceiverDownReason
RECEIVER_DOWN_REASON_NEARLY_FULL: ReceiverDownReason
RECEIVER_DOWN_REASON_TENANT_NOT_READY: ReceiverDownReason
RECEIVER_DOWN_REASON_STOPPING: ReceiverDownReason
RECEIVER_DOWN_REASON_OTHER: ReceiverDownReason
RECEIVER_DOWN_REASON_INCOMING_LIMIT: ReceiverDownReason

class Receiver(_message.Message):
    __slots__ = ("name", "dimse", "stow", "enabled", "storage", "naming", "quiet_period", "max_open_time", "max_file_bytes", "health", "create_time", "update_time", "forward_to", "as_received", "profile")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DIMSE_FIELD_NUMBER: _ClassVar[int]
    STOW_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    NAMING_FIELD_NUMBER: _ClassVar[int]
    QUIET_PERIOD_FIELD_NUMBER: _ClassVar[int]
    MAX_OPEN_TIME_FIELD_NUMBER: _ClassVar[int]
    MAX_FILE_BYTES_FIELD_NUMBER: _ClassVar[int]
    HEALTH_FIELD_NUMBER: _ClassVar[int]
    CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    UPDATE_TIME_FIELD_NUMBER: _ClassVar[int]
    FORWARD_TO_FIELD_NUMBER: _ClassVar[int]
    AS_RECEIVED_FIELD_NUMBER: _ClassVar[int]
    PROFILE_FIELD_NUMBER: _ClassVar[int]
    name: str
    dimse: DimseReceiver
    stow: StowReceiver
    enabled: bool
    storage: str
    naming: NamingRule
    quiet_period: _duration_pb2.Duration
    max_open_time: _duration_pb2.Duration
    max_file_bytes: int
    health: ReceiverHealth
    create_time: _timestamp_pb2.Timestamp
    update_time: _timestamp_pb2.Timestamp
    forward_to: _containers.RepeatedScalarFieldContainer[str]
    as_received: AsReceived
    profile: ProfileVersionRef
    def __init__(self, name: _Optional[str] = ..., dimse: _Optional[_Union[DimseReceiver, _Mapping]] = ..., stow: _Optional[_Union[StowReceiver, _Mapping]] = ..., enabled: _Optional[bool] = ..., storage: _Optional[str] = ..., naming: _Optional[_Union[NamingRule, _Mapping]] = ..., quiet_period: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., max_open_time: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., max_file_bytes: _Optional[int] = ..., health: _Optional[_Union[ReceiverHealth, _Mapping]] = ..., create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., update_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., forward_to: _Optional[_Iterable[str]] = ..., as_received: _Optional[_Union[AsReceived, _Mapping]] = ..., profile: _Optional[_Union[ProfileVersionRef, _Mapping]] = ...) -> None: ...

class AsReceived(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ProfileVersionRef(_message.Message):
    __slots__ = ("name", "version")
    NAME_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    name: str
    version: int
    def __init__(self, name: _Optional[str] = ..., version: _Optional[int] = ...) -> None: ...

class DimseReceiver(_message.Message):
    __slots__ = ("port", "bind_address", "ae_title", "allowed_callers", "any_caller", "tls", "sop_classes", "transfer_syntaxes")
    PORT_FIELD_NUMBER: _ClassVar[int]
    BIND_ADDRESS_FIELD_NUMBER: _ClassVar[int]
    AE_TITLE_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_CALLERS_FIELD_NUMBER: _ClassVar[int]
    ANY_CALLER_FIELD_NUMBER: _ClassVar[int]
    TLS_FIELD_NUMBER: _ClassVar[int]
    SOP_CLASSES_FIELD_NUMBER: _ClassVar[int]
    TRANSFER_SYNTAXES_FIELD_NUMBER: _ClassVar[int]
    port: int
    bind_address: str
    ae_title: str
    allowed_callers: AllowedCallers
    any_caller: bool
    tls: ReceiverTls
    sop_classes: _containers.RepeatedScalarFieldContainer[str]
    transfer_syntaxes: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, port: _Optional[int] = ..., bind_address: _Optional[str] = ..., ae_title: _Optional[str] = ..., allowed_callers: _Optional[_Union[AllowedCallers, _Mapping]] = ..., any_caller: _Optional[bool] = ..., tls: _Optional[_Union[ReceiverTls, _Mapping]] = ..., sop_classes: _Optional[_Iterable[str]] = ..., transfer_syntaxes: _Optional[_Iterable[str]] = ...) -> None: ...

class StowReceiver(_message.Message):
    __slots__ = ("key_ids", "max_request_bytes")
    KEY_IDS_FIELD_NUMBER: _ClassVar[int]
    MAX_REQUEST_BYTES_FIELD_NUMBER: _ClassVar[int]
    key_ids: _containers.RepeatedScalarFieldContainer[str]
    max_request_bytes: int
    def __init__(self, key_ids: _Optional[_Iterable[str]] = ..., max_request_bytes: _Optional[int] = ...) -> None: ...

class AllowedCallers(_message.Message):
    __slots__ = ("rules",)
    RULES_FIELD_NUMBER: _ClassVar[int]
    rules: _containers.RepeatedCompositeFieldContainer[CallerRule]
    def __init__(self, rules: _Optional[_Iterable[_Union[CallerRule, _Mapping]]] = ...) -> None: ...

class CallerRule(_message.Message):
    __slots__ = ("calling_ae_title", "address_range")
    CALLING_AE_TITLE_FIELD_NUMBER: _ClassVar[int]
    ADDRESS_RANGE_FIELD_NUMBER: _ClassVar[int]
    calling_ae_title: str
    address_range: str
    def __init__(self, calling_ae_title: _Optional[str] = ..., address_range: _Optional[str] = ...) -> None: ...

class ReceiverTls(_message.Message):
    __slots__ = ("certificate_file", "private_key_file")
    CERTIFICATE_FILE_FIELD_NUMBER: _ClassVar[int]
    PRIVATE_KEY_FILE_FIELD_NUMBER: _ClassVar[int]
    certificate_file: str
    private_key_file: str
    def __init__(self, certificate_file: _Optional[str] = ..., private_key_file: _Optional[str] = ...) -> None: ...

class NamingRule(_message.Message):
    __slots__ = ("subject", "session", "scan", "pattern")
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    SESSION_FIELD_NUMBER: _ClassVar[int]
    SCAN_FIELD_NUMBER: _ClassVar[int]
    PATTERN_FIELD_NUMBER: _ClassVar[int]
    subject: str
    session: str
    scan: str
    pattern: NamingPattern
    def __init__(self, subject: _Optional[str] = ..., session: _Optional[str] = ..., scan: _Optional[str] = ..., pattern: _Optional[_Union[NamingPattern, _Mapping]] = ...) -> None: ...

class NamingPattern(_message.Message):
    __slots__ = ("attribute", "expression")
    ATTRIBUTE_FIELD_NUMBER: _ClassVar[int]
    EXPRESSION_FIELD_NUMBER: _ClassVar[int]
    attribute: str
    expression: str
    def __init__(self, attribute: _Optional[str] = ..., expression: _Optional[str] = ...) -> None: ...

class ReceiverHealth(_message.Message):
    __slots__ = ("state", "down_reason", "detail", "since")
    STATE_FIELD_NUMBER: _ClassVar[int]
    DOWN_REASON_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    SINCE_FIELD_NUMBER: _ClassVar[int]
    state: ReceiverState
    down_reason: ReceiverDownReason
    detail: str
    since: _timestamp_pb2.Timestamp
    def __init__(self, state: _Optional[_Union[ReceiverState, str]] = ..., down_reason: _Optional[_Union[ReceiverDownReason, str]] = ..., detail: _Optional[str] = ..., since: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class AddReceiverRequest(_message.Message):
    __slots__ = ("tenant", "receiver")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    receiver: Receiver
    def __init__(self, tenant: _Optional[str] = ..., receiver: _Optional[_Union[Receiver, _Mapping]] = ...) -> None: ...

class AddReceiverResponse(_message.Message):
    __slots__ = ("receiver",)
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    receiver: Receiver
    def __init__(self, receiver: _Optional[_Union[Receiver, _Mapping]] = ...) -> None: ...

class GetReceiverRequest(_message.Message):
    __slots__ = ("tenant", "name")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class GetReceiverResponse(_message.Message):
    __slots__ = ("receiver",)
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    receiver: Receiver
    def __init__(self, receiver: _Optional[_Union[Receiver, _Mapping]] = ...) -> None: ...

class ListReceiversRequest(_message.Message):
    __slots__ = ("tenant", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListReceiversResponse(_message.Message):
    __slots__ = ("receivers", "next_page_token")
    RECEIVERS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    receivers: _containers.RepeatedCompositeFieldContainer[Receiver]
    next_page_token: str
    def __init__(self, receivers: _Optional[_Iterable[_Union[Receiver, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class SetReceiverRequest(_message.Message):
    __slots__ = ("tenant", "receiver", "update_mask")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    UPDATE_MASK_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    receiver: Receiver
    update_mask: _field_mask_pb2.FieldMask
    def __init__(self, tenant: _Optional[str] = ..., receiver: _Optional[_Union[Receiver, _Mapping]] = ..., update_mask: _Optional[_Union[_field_mask_pb2.FieldMask, _Mapping]] = ...) -> None: ...

class SetReceiverResponse(_message.Message):
    __slots__ = ("receiver",)
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    receiver: Receiver
    def __init__(self, receiver: _Optional[_Union[Receiver, _Mapping]] = ...) -> None: ...

class RemoveReceiverRequest(_message.Message):
    __slots__ = ("tenant", "name")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class RemoveReceiverResponse(_message.Message):
    __slots__ = ("receiver",)
    RECEIVER_FIELD_NUMBER: _ClassVar[int]
    receiver: Receiver
    def __init__(self, receiver: _Optional[_Union[Receiver, _Mapping]] = ...) -> None: ...
