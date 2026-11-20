import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class UnnamedElements(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    UNNAMED_ELEMENTS_UNSPECIFIED: _ClassVar[UnnamedElements]
    UNNAMED_ELEMENTS_KEEP: _ClassVar[UnnamedElements]
    UNNAMED_ELEMENTS_REMOVE: _ClassVar[UnnamedElements]

class ConditionSubject(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CONDITION_SUBJECT_UNSPECIFIED: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_MODALITY: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_SOP_CLASS_UID: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_MANUFACTURER: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_MODEL_NAME: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_SOFTWARE_VERSIONS: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_RECEIVER: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_CALLING_AE_TITLE: _ClassVar[ConditionSubject]
    CONDITION_SUBJECT_CALLED_AE_TITLE: _ClassVar[ConditionSubject]

class LookupMissing(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    LOOKUP_MISSING_UNSPECIFIED: _ClassVar[LookupMissing]
    LOOKUP_MISSING_QUARANTINE: _ClassVar[LookupMissing]
    LOOKUP_MISSING_ASSIGN: _ClassVar[LookupMissing]

class RequiredFallback(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REQUIRED_FALLBACK_UNSPECIFIED: _ClassVar[RequiredFallback]
    REQUIRED_FALLBACK_EMPTY: _ClassVar[RequiredFallback]
    REQUIRED_FALLBACK_DUMMY: _ClassVar[RequiredFallback]
    REQUIRED_FALLBACK_UID: _ClassVar[RequiredFallback]

class ProfileView(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    PROFILE_VIEW_UNSPECIFIED: _ClassVar[ProfileView]
    PROFILE_VIEW_DOCUMENT: _ClassVar[ProfileView]
    PROFILE_VIEW_FULL: _ClassVar[ProfileView]
UNNAMED_ELEMENTS_UNSPECIFIED: UnnamedElements
UNNAMED_ELEMENTS_KEEP: UnnamedElements
UNNAMED_ELEMENTS_REMOVE: UnnamedElements
CONDITION_SUBJECT_UNSPECIFIED: ConditionSubject
CONDITION_SUBJECT_MODALITY: ConditionSubject
CONDITION_SUBJECT_SOP_CLASS_UID: ConditionSubject
CONDITION_SUBJECT_MANUFACTURER: ConditionSubject
CONDITION_SUBJECT_MODEL_NAME: ConditionSubject
CONDITION_SUBJECT_SOFTWARE_VERSIONS: ConditionSubject
CONDITION_SUBJECT_RECEIVER: ConditionSubject
CONDITION_SUBJECT_CALLING_AE_TITLE: ConditionSubject
CONDITION_SUBJECT_CALLED_AE_TITLE: ConditionSubject
LOOKUP_MISSING_UNSPECIFIED: LookupMissing
LOOKUP_MISSING_QUARANTINE: LookupMissing
LOOKUP_MISSING_ASSIGN: LookupMissing
REQUIRED_FALLBACK_UNSPECIFIED: RequiredFallback
REQUIRED_FALLBACK_EMPTY: RequiredFallback
REQUIRED_FALLBACK_DUMMY: RequiredFallback
REQUIRED_FALLBACK_UID: RequiredFallback
PROFILE_VIEW_UNSPECIFIED: ProfileView
PROFILE_VIEW_DOCUMENT: ProfileView
PROFILE_VIEW_FULL: ProfileView

class ProfileDocument(_message.Message):
    __slots__ = ("bases", "rules", "unnamed", "date_shift_days", "pixel_rules", "description")
    BASES_FIELD_NUMBER: _ClassVar[int]
    RULES_FIELD_NUMBER: _ClassVar[int]
    UNNAMED_FIELD_NUMBER: _ClassVar[int]
    DATE_SHIFT_DAYS_FIELD_NUMBER: _ClassVar[int]
    PIXEL_RULES_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    bases: _containers.RepeatedScalarFieldContainer[str]
    rules: _containers.RepeatedCompositeFieldContainer[ProfileRule]
    unnamed: UnnamedElements
    date_shift_days: int
    pixel_rules: _containers.RepeatedCompositeFieldContainer[PixelRule]
    description: str
    def __init__(self, bases: _Optional[_Iterable[str]] = ..., rules: _Optional[_Iterable[_Union[ProfileRule, _Mapping]]] = ..., unnamed: _Optional[_Union[UnnamedElements, str]] = ..., date_shift_days: _Optional[int] = ..., pixel_rules: _Optional[_Iterable[_Union[PixelRule, _Mapping]]] = ..., description: _Optional[str] = ...) -> None: ...

class ProfileRule(_message.Message):
    __slots__ = ("name", "selector", "when", "action", "reason")
    NAME_FIELD_NUMBER: _ClassVar[int]
    SELECTOR_FIELD_NUMBER: _ClassVar[int]
    WHEN_FIELD_NUMBER: _ClassVar[int]
    ACTION_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    name: str
    selector: ProfileSelector
    when: _containers.RepeatedCompositeFieldContainer[ProfileCondition]
    action: ProfileAction
    reason: str
    def __init__(self, name: _Optional[str] = ..., selector: _Optional[_Union[ProfileSelector, _Mapping]] = ..., when: _Optional[_Iterable[_Union[ProfileCondition, _Mapping]]] = ..., action: _Optional[_Union[ProfileAction, _Mapping]] = ..., reason: _Optional[str] = ...) -> None: ...

class ProfileSelector(_message.Message):
    __slots__ = ("tag", "keyword", "group", "vr", "private_element", "path", "content_item", "include_private")
    TAG_FIELD_NUMBER: _ClassVar[int]
    KEYWORD_FIELD_NUMBER: _ClassVar[int]
    GROUP_FIELD_NUMBER: _ClassVar[int]
    VR_FIELD_NUMBER: _ClassVar[int]
    PRIVATE_ELEMENT_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    CONTENT_ITEM_FIELD_NUMBER: _ClassVar[int]
    INCLUDE_PRIVATE_FIELD_NUMBER: _ClassVar[int]
    tag: str
    keyword: str
    group: str
    vr: str
    private_element: PrivateElement
    path: str
    content_item: ContentItemSelector
    include_private: bool
    def __init__(self, tag: _Optional[str] = ..., keyword: _Optional[str] = ..., group: _Optional[str] = ..., vr: _Optional[str] = ..., private_element: _Optional[_Union[PrivateElement, _Mapping]] = ..., path: _Optional[str] = ..., content_item: _Optional[_Union[ContentItemSelector, _Mapping]] = ..., include_private: _Optional[bool] = ...) -> None: ...

class PrivateElement(_message.Message):
    __slots__ = ("creator", "group", "element")
    CREATOR_FIELD_NUMBER: _ClassVar[int]
    GROUP_FIELD_NUMBER: _ClassVar[int]
    ELEMENT_FIELD_NUMBER: _ClassVar[int]
    creator: str
    group: str
    element: str
    def __init__(self, creator: _Optional[str] = ..., group: _Optional[str] = ..., element: _Optional[str] = ...) -> None: ...

class ContentItemSelector(_message.Message):
    __slots__ = ("scheme", "value", "value_type")
    SCHEME_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    VALUE_TYPE_FIELD_NUMBER: _ClassVar[int]
    scheme: str
    value: str
    value_type: str
    def __init__(self, scheme: _Optional[str] = ..., value: _Optional[str] = ..., value_type: _Optional[str] = ...) -> None: ...

class ProfileCondition(_message.Message):
    __slots__ = ("subject", "equals", "prefix", "pattern", "present", "negate")
    SUBJECT_FIELD_NUMBER: _ClassVar[int]
    EQUALS_FIELD_NUMBER: _ClassVar[int]
    PREFIX_FIELD_NUMBER: _ClassVar[int]
    PATTERN_FIELD_NUMBER: _ClassVar[int]
    PRESENT_FIELD_NUMBER: _ClassVar[int]
    NEGATE_FIELD_NUMBER: _ClassVar[int]
    subject: ConditionSubject
    equals: str
    prefix: str
    pattern: str
    present: bool
    negate: bool
    def __init__(self, subject: _Optional[_Union[ConditionSubject, str]] = ..., equals: _Optional[str] = ..., prefix: _Optional[str] = ..., pattern: _Optional[str] = ..., present: _Optional[bool] = ..., negate: _Optional[bool] = ...) -> None: ...

class ProfileAction(_message.Message):
    __slots__ = ("keep", "remove", "empty", "replace", "set", "hash", "uid", "shift", "generalize", "lookup", "clean", "quarantine", "dummy", "remove_unless_required")
    KEEP_FIELD_NUMBER: _ClassVar[int]
    REMOVE_FIELD_NUMBER: _ClassVar[int]
    EMPTY_FIELD_NUMBER: _ClassVar[int]
    REPLACE_FIELD_NUMBER: _ClassVar[int]
    SET_FIELD_NUMBER: _ClassVar[int]
    HASH_FIELD_NUMBER: _ClassVar[int]
    UID_FIELD_NUMBER: _ClassVar[int]
    SHIFT_FIELD_NUMBER: _ClassVar[int]
    GENERALIZE_FIELD_NUMBER: _ClassVar[int]
    LOOKUP_FIELD_NUMBER: _ClassVar[int]
    CLEAN_FIELD_NUMBER: _ClassVar[int]
    QUARANTINE_FIELD_NUMBER: _ClassVar[int]
    DUMMY_FIELD_NUMBER: _ClassVar[int]
    REMOVE_UNLESS_REQUIRED_FIELD_NUMBER: _ClassVar[int]
    keep: KeepAction
    remove: RemoveAction
    empty: EmptyAction
    replace: ReplaceAction
    set: SetAction
    hash: HashAction
    uid: UidAction
    shift: ShiftAction
    generalize: GeneralizeAction
    lookup: LookupAction
    clean: CleanAction
    quarantine: QuarantineAction
    dummy: DummyAction
    remove_unless_required: RemoveUnlessRequiredAction
    def __init__(self, keep: _Optional[_Union[KeepAction, _Mapping]] = ..., remove: _Optional[_Union[RemoveAction, _Mapping]] = ..., empty: _Optional[_Union[EmptyAction, _Mapping]] = ..., replace: _Optional[_Union[ReplaceAction, _Mapping]] = ..., set: _Optional[_Union[SetAction, _Mapping]] = ..., hash: _Optional[_Union[HashAction, _Mapping]] = ..., uid: _Optional[_Union[UidAction, _Mapping]] = ..., shift: _Optional[_Union[ShiftAction, _Mapping]] = ..., generalize: _Optional[_Union[GeneralizeAction, _Mapping]] = ..., lookup: _Optional[_Union[LookupAction, _Mapping]] = ..., clean: _Optional[_Union[CleanAction, _Mapping]] = ..., quarantine: _Optional[_Union[QuarantineAction, _Mapping]] = ..., dummy: _Optional[_Union[DummyAction, _Mapping]] = ..., remove_unless_required: _Optional[_Union[RemoveUnlessRequiredAction, _Mapping]] = ...) -> None: ...

class KeepAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RemoveAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class EmptyAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ReplaceAction(_message.Message):
    __slots__ = ("constant", "template")
    CONSTANT_FIELD_NUMBER: _ClassVar[int]
    TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    constant: str
    template: str
    def __init__(self, constant: _Optional[str] = ..., template: _Optional[str] = ...) -> None: ...

class SetAction(_message.Message):
    __slots__ = ("constant", "template")
    CONSTANT_FIELD_NUMBER: _ClassVar[int]
    TEMPLATE_FIELD_NUMBER: _ClassVar[int]
    constant: str
    template: str
    def __init__(self, constant: _Optional[str] = ..., template: _Optional[str] = ...) -> None: ...

class HashAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class UidAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ShiftAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class GeneralizeAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class LookupAction(_message.Message):
    __slots__ = ("table", "on_missing")
    TABLE_FIELD_NUMBER: _ClassVar[int]
    ON_MISSING_FIELD_NUMBER: _ClassVar[int]
    table: str
    on_missing: LookupMissing
    def __init__(self, table: _Optional[str] = ..., on_missing: _Optional[_Union[LookupMissing, str]] = ...) -> None: ...

class CleanAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class QuarantineAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class DummyAction(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RemoveUnlessRequiredAction(_message.Message):
    __slots__ = ("fallback",)
    FALLBACK_FIELD_NUMBER: _ClassVar[int]
    fallback: RequiredFallback
    def __init__(self, fallback: _Optional[_Union[RequiredFallback, str]] = ...) -> None: ...

class PixelRule(_message.Message):
    __slots__ = ("name", "when", "rows", "columns", "redact", "detect", "accept", "reason")
    NAME_FIELD_NUMBER: _ClassVar[int]
    WHEN_FIELD_NUMBER: _ClassVar[int]
    ROWS_FIELD_NUMBER: _ClassVar[int]
    COLUMNS_FIELD_NUMBER: _ClassVar[int]
    REDACT_FIELD_NUMBER: _ClassVar[int]
    DETECT_FIELD_NUMBER: _ClassVar[int]
    ACCEPT_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    name: str
    when: _containers.RepeatedCompositeFieldContainer[ProfileCondition]
    rows: int
    columns: int
    redact: RedactPixels
    detect: DetectText
    accept: AcceptPixels
    reason: str
    def __init__(self, name: _Optional[str] = ..., when: _Optional[_Iterable[_Union[ProfileCondition, _Mapping]]] = ..., rows: _Optional[int] = ..., columns: _Optional[int] = ..., redact: _Optional[_Union[RedactPixels, _Mapping]] = ..., detect: _Optional[_Union[DetectText, _Mapping]] = ..., accept: _Optional[_Union[AcceptPixels, _Mapping]] = ..., reason: _Optional[str] = ...) -> None: ...

class RedactPixels(_message.Message):
    __slots__ = ("rectangles",)
    RECTANGLES_FIELD_NUMBER: _ClassVar[int]
    rectangles: _containers.RepeatedCompositeFieldContainer[PixelRectangle]
    def __init__(self, rectangles: _Optional[_Iterable[_Union[PixelRectangle, _Mapping]]] = ...) -> None: ...

class PixelRectangle(_message.Message):
    __slots__ = ("x", "y", "width", "height", "from_right", "from_bottom")
    X_FIELD_NUMBER: _ClassVar[int]
    Y_FIELD_NUMBER: _ClassVar[int]
    WIDTH_FIELD_NUMBER: _ClassVar[int]
    HEIGHT_FIELD_NUMBER: _ClassVar[int]
    FROM_RIGHT_FIELD_NUMBER: _ClassVar[int]
    FROM_BOTTOM_FIELD_NUMBER: _ClassVar[int]
    x: int
    y: int
    width: int
    height: int
    from_right: bool
    from_bottom: bool
    def __init__(self, x: _Optional[int] = ..., y: _Optional[int] = ..., width: _Optional[int] = ..., height: _Optional[int] = ..., from_right: _Optional[bool] = ..., from_bottom: _Optional[bool] = ...) -> None: ...

class DetectText(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class AcceptPixels(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ExpandedProfile(_message.Message):
    __slots__ = ("rules", "unnamed", "date_shift_days", "pixel_rules", "bases", "language_edition")
    RULES_FIELD_NUMBER: _ClassVar[int]
    UNNAMED_FIELD_NUMBER: _ClassVar[int]
    DATE_SHIFT_DAYS_FIELD_NUMBER: _ClassVar[int]
    PIXEL_RULES_FIELD_NUMBER: _ClassVar[int]
    BASES_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_EDITION_FIELD_NUMBER: _ClassVar[int]
    rules: _containers.RepeatedCompositeFieldContainer[ExpandedRule]
    unnamed: UnnamedElements
    date_shift_days: int
    pixel_rules: _containers.RepeatedCompositeFieldContainer[ExpandedPixelRule]
    bases: _containers.RepeatedScalarFieldContainer[str]
    language_edition: int
    def __init__(self, rules: _Optional[_Iterable[_Union[ExpandedRule, _Mapping]]] = ..., unnamed: _Optional[_Union[UnnamedElements, str]] = ..., date_shift_days: _Optional[int] = ..., pixel_rules: _Optional[_Iterable[_Union[ExpandedPixelRule, _Mapping]]] = ..., bases: _Optional[_Iterable[str]] = ..., language_edition: _Optional[int] = ...) -> None: ...

class ExpandedRule(_message.Message):
    __slots__ = ("rule", "origin", "vrs")
    RULE_FIELD_NUMBER: _ClassVar[int]
    ORIGIN_FIELD_NUMBER: _ClassVar[int]
    VRS_FIELD_NUMBER: _ClassVar[int]
    rule: ProfileRule
    origin: str
    vrs: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, rule: _Optional[_Union[ProfileRule, _Mapping]] = ..., origin: _Optional[str] = ..., vrs: _Optional[_Iterable[str]] = ...) -> None: ...

class ExpandedPixelRule(_message.Message):
    __slots__ = ("rule", "origin")
    RULE_FIELD_NUMBER: _ClassVar[int]
    ORIGIN_FIELD_NUMBER: _ClassVar[int]
    rule: PixelRule
    origin: str
    def __init__(self, rule: _Optional[_Union[PixelRule, _Mapping]] = ..., origin: _Optional[str] = ...) -> None: ...

class ProfileVersion(_message.Message):
    __slots__ = ("name", "version", "checksum", "create_time", "created_by_key_id", "note", "document", "expanded")
    NAME_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CHECKSUM_FIELD_NUMBER: _ClassVar[int]
    CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_KEY_ID_FIELD_NUMBER: _ClassVar[int]
    NOTE_FIELD_NUMBER: _ClassVar[int]
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    EXPANDED_FIELD_NUMBER: _ClassVar[int]
    name: str
    version: int
    checksum: str
    create_time: _timestamp_pb2.Timestamp
    created_by_key_id: str
    note: str
    document: ProfileDocument
    expanded: ExpandedProfile
    def __init__(self, name: _Optional[str] = ..., version: _Optional[int] = ..., checksum: _Optional[str] = ..., create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., created_by_key_id: _Optional[str] = ..., note: _Optional[str] = ..., document: _Optional[_Union[ProfileDocument, _Mapping]] = ..., expanded: _Optional[_Union[ExpandedProfile, _Mapping]] = ...) -> None: ...

class ProfileSummary(_message.Message):
    __slots__ = ("name", "latest_version", "latest_create_time")
    NAME_FIELD_NUMBER: _ClassVar[int]
    LATEST_VERSION_FIELD_NUMBER: _ClassVar[int]
    LATEST_CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    name: str
    latest_version: int
    latest_create_time: _timestamp_pb2.Timestamp
    def __init__(self, name: _Optional[str] = ..., latest_version: _Optional[int] = ..., latest_create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class ProfileProblem(_message.Message):
    __slots__ = ("rule", "pixel_rule", "rule_name", "field", "code", "message")
    RULE_FIELD_NUMBER: _ClassVar[int]
    PIXEL_RULE_FIELD_NUMBER: _ClassVar[int]
    RULE_NAME_FIELD_NUMBER: _ClassVar[int]
    FIELD_FIELD_NUMBER: _ClassVar[int]
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    rule: int
    pixel_rule: int
    rule_name: str
    field: str
    code: str
    message: str
    def __init__(self, rule: _Optional[int] = ..., pixel_rule: _Optional[int] = ..., rule_name: _Optional[str] = ..., field: _Optional[str] = ..., code: _Optional[str] = ..., message: _Optional[str] = ...) -> None: ...

class ProfileProblems(_message.Message):
    __slots__ = ("problems",)
    PROBLEMS_FIELD_NUMBER: _ClassVar[int]
    problems: _containers.RepeatedCompositeFieldContainer[ProfileProblem]
    def __init__(self, problems: _Optional[_Iterable[_Union[ProfileProblem, _Mapping]]] = ...) -> None: ...

class ProfileBase(_message.Message):
    __slots__ = ("name", "available", "unavailable_reason", "description", "document")
    NAME_FIELD_NUMBER: _ClassVar[int]
    AVAILABLE_FIELD_NUMBER: _ClassVar[int]
    UNAVAILABLE_REASON_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    name: str
    available: bool
    unavailable_reason: str
    description: str
    document: ProfileDocument
    def __init__(self, name: _Optional[str] = ..., available: _Optional[bool] = ..., unavailable_reason: _Optional[str] = ..., description: _Optional[str] = ..., document: _Optional[_Union[ProfileDocument, _Mapping]] = ...) -> None: ...

class CreateProfileVersionRequest(_message.Message):
    __slots__ = ("tenant", "name", "document", "note", "expected_latest_version", "view")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    NOTE_FIELD_NUMBER: _ClassVar[int]
    EXPECTED_LATEST_VERSION_FIELD_NUMBER: _ClassVar[int]
    VIEW_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    document: ProfileDocument
    note: str
    expected_latest_version: int
    view: ProfileView
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ..., document: _Optional[_Union[ProfileDocument, _Mapping]] = ..., note: _Optional[str] = ..., expected_latest_version: _Optional[int] = ..., view: _Optional[_Union[ProfileView, str]] = ...) -> None: ...

class CreateProfileVersionResponse(_message.Message):
    __slots__ = ("version", "created", "warnings", "note_differs")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    CREATED_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    NOTE_DIFFERS_FIELD_NUMBER: _ClassVar[int]
    version: ProfileVersion
    created: bool
    warnings: _containers.RepeatedCompositeFieldContainer[ProfileProblem]
    note_differs: bool
    def __init__(self, version: _Optional[_Union[ProfileVersion, _Mapping]] = ..., created: _Optional[bool] = ..., warnings: _Optional[_Iterable[_Union[ProfileProblem, _Mapping]]] = ..., note_differs: _Optional[bool] = ...) -> None: ...

class GetProfileVersionRequest(_message.Message):
    __slots__ = ("tenant", "name", "version", "view")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    VIEW_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    version: int
    view: ProfileView
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ..., version: _Optional[int] = ..., view: _Optional[_Union[ProfileView, str]] = ...) -> None: ...

class GetProfileVersionResponse(_message.Message):
    __slots__ = ("version",)
    VERSION_FIELD_NUMBER: _ClassVar[int]
    version: ProfileVersion
    def __init__(self, version: _Optional[_Union[ProfileVersion, _Mapping]] = ...) -> None: ...

class ListProfilesRequest(_message.Message):
    __slots__ = ("tenant", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListProfilesResponse(_message.Message):
    __slots__ = ("profiles", "next_page_token")
    PROFILES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    profiles: _containers.RepeatedCompositeFieldContainer[ProfileSummary]
    next_page_token: str
    def __init__(self, profiles: _Optional[_Iterable[_Union[ProfileSummary, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class ListProfileVersionsRequest(_message.Message):
    __slots__ = ("tenant", "name", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListProfileVersionsResponse(_message.Message):
    __slots__ = ("versions", "next_page_token")
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    versions: _containers.RepeatedCompositeFieldContainer[ProfileVersion]
    next_page_token: str
    def __init__(self, versions: _Optional[_Iterable[_Union[ProfileVersion, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class ValidateProfileRequest(_message.Message):
    __slots__ = ("tenant", "document", "view")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    DOCUMENT_FIELD_NUMBER: _ClassVar[int]
    VIEW_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    document: ProfileDocument
    view: ProfileView
    def __init__(self, tenant: _Optional[str] = ..., document: _Optional[_Union[ProfileDocument, _Mapping]] = ..., view: _Optional[_Union[ProfileView, str]] = ...) -> None: ...

class ValidateProfileResponse(_message.Message):
    __slots__ = ("problems", "expanded", "checksum", "warnings")
    PROBLEMS_FIELD_NUMBER: _ClassVar[int]
    EXPANDED_FIELD_NUMBER: _ClassVar[int]
    CHECKSUM_FIELD_NUMBER: _ClassVar[int]
    WARNINGS_FIELD_NUMBER: _ClassVar[int]
    problems: _containers.RepeatedCompositeFieldContainer[ProfileProblem]
    expanded: ExpandedProfile
    checksum: str
    warnings: _containers.RepeatedCompositeFieldContainer[ProfileProblem]
    def __init__(self, problems: _Optional[_Iterable[_Union[ProfileProblem, _Mapping]]] = ..., expanded: _Optional[_Union[ExpandedProfile, _Mapping]] = ..., checksum: _Optional[str] = ..., warnings: _Optional[_Iterable[_Union[ProfileProblem, _Mapping]]] = ...) -> None: ...

class ListProfileBasesRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListProfileBasesResponse(_message.Message):
    __slots__ = ("bases",)
    BASES_FIELD_NUMBER: _ClassVar[int]
    bases: _containers.RepeatedCompositeFieldContainer[ProfileBase]
    def __init__(self, bases: _Optional[_Iterable[_Union[ProfileBase, _Mapping]]] = ...) -> None: ...
