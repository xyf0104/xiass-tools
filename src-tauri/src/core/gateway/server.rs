use std::collections::HashMap;
use std::io::Read;
use std::net::TcpListener;
use std::net::TcpStream;
use std::sync::mpsc::Receiver;
use std::sync::Arc;
use std::thread::{self, JoinHandle};
use std::time::Duration;

// Image inputs are commonly sent as base64 data URLs, so the old 1 MiB limit
// rejected ordinary multimodal requests. This matches the 32 MB request limit
// of the upstream providers we forward to, so an oversized request is refused
// here with a clear local error instead of failing remotely.
const MAX_REQUEST_BYTES: usize = 32 * 1024 * 1024;

pub(in crate::core::gateway) fn spawn_accept_loop<C, E>(
    listener: TcpListener,
    shutdown: Receiver<()>,
    on_connection: C,
    on_error: E,
) -> JoinHandle<()>
where
    C: Fn(TcpStream) + Send + Sync + 'static,
    E: Fn(String) + Send + Sync + 'static,
{
    let on_connection = Arc::new(on_connection);
    thread::spawn(move || loop {
        if shutdown.try_recv().is_ok() {
            break;
        }
        match listener.accept() {
            Ok((stream, _)) => {
                let on_connection = Arc::clone(&on_connection);
                thread::spawn(move || on_connection(stream));
            }
            Err(err) if err.kind() == std::io::ErrorKind::WouldBlock => {
                thread::sleep(Duration::from_millis(35));
            }
            Err(err) => {
                on_error(format!("Gateway accept failed: {err}"));
                thread::sleep(Duration::from_millis(100));
            }
        }
    })
}

pub(in crate::core::gateway) struct HttpRequest {
    pub(in crate::core::gateway) method: String,
    pub(in crate::core::gateway) path: String,
    pub(in crate::core::gateway) headers: HashMap<String, String>,
    pub(in crate::core::gateway) body: Vec<u8>,
}

pub(in crate::core::gateway) struct HttpResponse {
    pub(in crate::core::gateway) status: u16,
    pub(in crate::core::gateway) reason: &'static str,
    pub(in crate::core::gateway) content_type: &'static str,
    pub(in crate::core::gateway) headers: Vec<(&'static str, &'static str)>,
    pub(in crate::core::gateway) body: Vec<u8>,
}

pub(in crate::core::gateway) enum RouteResponse {
    Buffered(HttpResponse),
    Stream(StreamingResponse),
}

impl RouteResponse {
    pub(in crate::core::gateway) fn status(&self) -> u16 {
        match self {
            Self::Buffered(response) => response.status,
            Self::Stream(response) => response.expected_status,
        }
    }
}

pub(in crate::core::gateway) struct StreamingResponse {
    pub(in crate::core::gateway) expected_status: u16,
    pub(in crate::core::gateway) run: Box<dyn FnOnce(&mut TcpStream) -> Result<u16, String> + Send>,
}

pub(in crate::core::gateway) fn write_buffered_response(
    stream: &mut TcpStream,
    response: HttpResponse,
) -> Result<u16, String> {
    use std::io::Write;
    let status = response.status;
    let mut head = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: {}\r\nContent-Length: {}\r\nConnection: close\r\n",
        response.status,
        response.reason,
        response.content_type,
        response.body.len()
    );
    append_cors_headers(&mut head);
    for (name, value) in response.headers {
        head.push_str(name);
        head.push_str(": ");
        head.push_str(value);
        head.push_str("\r\n");
    }
    head.push_str("\r\n");
    stream
        .write_all(head.as_bytes())
        .and_then(|_| stream.write_all(&response.body))
        .and_then(|_| stream.flush())
        .map_err(|err| err.to_string())?;
    Ok(status)
}

pub(in crate::core::gateway) fn write_route_response(
    stream: &mut TcpStream,
    response: RouteResponse,
) -> Result<u16, String> {
    match response {
        RouteResponse::Buffered(response) => write_buffered_response(stream, response),
        RouteResponse::Stream(response) => (response.run)(stream),
    }
}

pub(in crate::core::gateway) fn write_stream_headers(
    stream: &mut TcpStream,
    status: u16,
    reason: &'static str,
    content_type: &'static str,
) -> Result<(), String> {
    use std::io::Write;
    let mut head = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: {}\r\nCache-Control: no-cache\r\nConnection: close\r\nX-Accel-Buffering: no\r\n",
        status, reason, content_type
    );
    append_cors_headers(&mut head);
    head.push_str("\r\n");
    stream
        .write_all(head.as_bytes())
        .and_then(|_| stream.flush())
        .map_err(|err| err.to_string())
}

pub(in crate::core::gateway) fn append_cors_headers(head: &mut String) {
    head.push_str("Access-Control-Allow-Origin: *\r\n");
    head.push_str("Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS\r\n");
    head.push_str("Access-Control-Allow-Headers: *\r\n");
}

pub(in crate::core::gateway) fn read_request(
    stream: &mut TcpStream,
) -> Result<HttpRequest, String> {
    read_request_with_limit(stream, MAX_REQUEST_BYTES)
}

fn read_request_with_limit(
    stream: &mut TcpStream,
    max_request_bytes: usize,
) -> Result<HttpRequest, String> {
    let mut buffer = Vec::new();
    let mut chunk = [0_u8; 4096];
    let mut header_end = None;
    let mut content_length = 0_usize;

    loop {
        let read = stream.read(&mut chunk).map_err(|err| err.to_string())?;
        if read == 0 {
            break;
        }
        buffer.extend_from_slice(&chunk[..read]);
        if buffer.len() > max_request_bytes {
            return Err("Request is too large".to_string());
        }
        if header_end.is_none() {
            if let Some(index) = find_header_end(&buffer) {
                header_end = Some(index + 4);
                content_length = parse_content_length(&String::from_utf8_lossy(&buffer[..index]));
            }
        }
        if header_end.is_some_and(|end| buffer.len() >= end + content_length) {
            break;
        }
    }

    let header_end = header_end.ok_or_else(|| "Invalid HTTP request".to_string())?;
    let header_text = String::from_utf8_lossy(&buffer[..header_end]);
    let mut lines = header_text.lines();
    let mut request_line = lines
        .next()
        .ok_or_else(|| "Missing HTTP request line".to_string())?
        .split_whitespace();
    let method = request_line.next().unwrap_or_default().to_string();
    let path = request_line.next().unwrap_or_default().to_string();
    let headers = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.trim().to_ascii_lowercase(), value.trim().to_string()))
        .collect();
    let end = header_end + content_length;
    let body = buffer
        .get(header_end..end.min(buffer.len()))
        .unwrap_or_default()
        .to_vec();

    Ok(HttpRequest {
        method,
        path,
        headers,
        body,
    })
}

fn find_header_end(buffer: &[u8]) -> Option<usize> {
    buffer.windows(4).position(|window| window == b"\r\n\r\n")
}

fn parse_content_length(headers: &str) -> usize {
    headers
        .lines()
        .filter_map(|line| line.split_once(':'))
        .find(|(name, _)| name.trim().eq_ignore_ascii_case("content-length"))
        .and_then(|(_, value)| value.trim().parse().ok())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::{read_request, read_request_with_limit, HttpRequest, MAX_REQUEST_BYTES};
    use std::io::Write;
    use std::net::{TcpListener, TcpStream};
    use std::thread;
    use std::time::Duration;

    fn post_body(body: Vec<u8>) -> Vec<u8> {
        let mut request = format!(
            "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
            body.len()
        )
        .into_bytes();
        request.extend_from_slice(&body);
        request
    }

    /// Send `body` as one POST over loopback and read it back with the given
    /// byte limit, exactly as the accept loop does for a real client.
    fn round_trip(body: Vec<u8>, max_request_bytes: Option<usize>) -> Result<HttpRequest, String> {
        let listener = TcpListener::bind("127.0.0.1:0").expect("loopback listener should bind");
        let address = listener
            .local_addr()
            .expect("listener should have an address");
        let request = post_body(body);
        let client = thread::spawn(move || {
            let mut stream = TcpStream::connect(address).expect("client should connect");
            // An over-limit request is rejected before the body finishes, so a
            // write error here is the expected outcome rather than a failure.
            let _ = stream.write_all(&request);
            let _ = stream.flush();
        });

        let (mut stream, _) = listener.accept().expect("server should accept");
        stream
            .set_read_timeout(Some(Duration::from_secs(30)))
            .expect("read timeout should apply");
        let result = match max_request_bytes {
            Some(limit) => read_request_with_limit(&mut stream, limit),
            None => read_request(&mut stream),
        };
        drop(stream);
        client.join().expect("client thread should finish");
        result
    }

    fn jpeg_data_url_body(payload_bytes: usize) -> Vec<u8> {
        let data = "A".repeat(payload_bytes);
        format!("{{\"image\":\"data:image/jpeg;base64,{data}\"}}").into_bytes()
    }

    #[test]
    fn max_request_bytes_matches_the_upstream_request_limit() {
        // Upstream providers cap a request body at 32 MB. Staying at that
        // number keeps the rejection local and the error message ours.
        assert_eq!(MAX_REQUEST_BYTES, 32 * 1024 * 1024);
        assert!(
            MAX_REQUEST_BYTES > 1024 * 1024,
            "the legacy 1 MiB cap is gone"
        );
    }

    #[test]
    fn read_request_accepts_an_image_body_beyond_the_legacy_one_megabyte_limit() {
        let body = jpeg_data_url_body(2 * 1024 * 1024);
        assert!(body.len() > 1024 * 1024);

        let request = round_trip(body.clone(), None).expect("a 2 MiB image body should be read");

        assert_eq!(request.method, "POST");
        assert_eq!(request.path, "/v1/chat/completions");
        assert_eq!(
            request.headers.get("content-length").map(String::as_str),
            Some(body.len().to_string().as_str())
        );
        assert_eq!(request.body.len(), body.len());
        assert_eq!(request.body, body);
    }

    #[test]
    fn read_request_rejects_a_body_over_the_limit() {
        let error = match round_trip(jpeg_data_url_body(4096), Some(1024)) {
            Ok(_) => panic!("a body over the limit should be rejected"),
            Err(error) => error,
        };

        assert_eq!(error, "Request is too large");
    }
}
