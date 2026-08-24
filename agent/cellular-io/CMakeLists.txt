cmake_minimum_required(VERSION 3.20)
project(mdd_cellular_io C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_OSX_DEPLOYMENT_TARGET "15.0")

if(NOT DEFINED LWIP_DIR OR NOT DEFINED LIBUSB_ROOT)
  message(FATAL_ERROR "Pass fixed extracted roots with -DLWIP_DIR=... -DLIBUSB_ROOT=...")
endif()
include("${LWIP_DIR}/src/Filelists.cmake")

add_executable(mdd-cellular-io
  mdd_cellular_io.c
  at_response.c
  ${lwipcore_SRCS}
  ${lwipcore4_SRCS}
  ${lwipppp_SRCS}
  "${LWIP_DIR}/src/netif/ethernet.c"
)

include(CTest)
if(BUILD_TESTING)
  add_executable(mdd-at-response-test at_response.c at_response_test.c)
  target_compile_options(mdd-at-response-test PRIVATE -O2 -Wall -Wextra -Werror)
  add_test(NAME mdd-at-response-test COMMAND mdd-at-response-test)
endif()
target_include_directories(mdd-cellular-io PRIVATE
  "${CMAKE_CURRENT_SOURCE_DIR}"
  "${LWIP_DIR}/src/include"
  "${LWIP_DIR}/contrib/ports/unix/port/include"
  "${LIBUSB_ROOT}/include/libusb-1.0"
)
target_compile_options(mdd-cellular-io PRIVATE
  -O2 -Wall -Wextra -Werror -Wno-unused-parameter
)
target_link_libraries(mdd-cellular-io PRIVATE
  "${LIBUSB_ROOT}/lib/libusb-1.0.a"
  objc
  "-framework IOKit"
  "-framework CoreFoundation"
  "-framework Security"
)
